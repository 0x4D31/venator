// Package engine implements one deterministic Venator rule execution.
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/0x4D31/venator/connector"
	"github.com/0x4D31/venator/connector/stdio"
	"github.com/0x4D31/venator/internal/config"
	"github.com/0x4D31/venator/internal/exclusion"
	"github.com/0x4D31/venator/internal/model"
	"github.com/0x4D31/venator/internal/signal"
)

type Registry interface {
	GetQueryRunner(name string) (connector.QueryRunner, error)
	GetPublisher(name string) (connector.Publisher, error)
}

const (
	defaultReviewTimeout  = 30 * time.Second
	minimumPublishReserve = 5 * time.Second
	defaultReviewLimit    = 50
)

// ReviewFunc can only return annotations keyed by finding ID. The engine owns
// the deterministic finding list and never accepts replacement records.
type ReviewFunc func(context.Context, []model.Finding, *config.RuleConfig) (map[string]model.Review, error)

type Options struct {
	RulePath      string
	Force         bool
	Review        ReviewFunc
	Now           func() time.Time
	MaxRecords    int
	MaxBytes      int64
	ReviewTimeout time.Duration
}

func Run(ctx context.Context, registry Registry, rule *config.RuleConfig, opts Options) (report model.RunReport, runErr error) {
	if rule == nil {
		return report, fmt.Errorf("rule is nil")
	}
	if rule.LLM != nil && rule.LLM.Required && !rule.LLM.Enabled {
		return report, fmt.Errorf("required LLM review is disabled")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	started := now().UTC()
	runID, err := model.NewRunID()
	if err != nil {
		return report, err
	}
	report = model.RunReport{
		SchemaVersion: model.RunSchemaVersion,
		RunID:         runID, RuleID: rule.UID, RuleName: rule.Name,
		Status: "running", StartedAt: started,
	}
	defer func() {
		report.FinishedAt = now().UTC()
		if runErr != nil {
			report.Status = "failed"
			report.Error = runErr.Error()
		} else if report.Status == "running" {
			report.Status = "succeeded"
		}
	}()

	if err := ctx.Err(); err != nil {
		return report, err
	}
	if !rule.Enabled && !opts.Force {
		report.Status = "skipped"
		return report, nil
	}

	excluder, err := loadExcluder(opts.RulePath, rule)
	if err != nil {
		return report, err
	}

	source, err := registry.GetQueryRunner(rule.QueryEngine)
	if err != nil {
		return report, fmt.Errorf("get source %q: %w", rule.QueryEngine, err)
	}
	records, err := source.Query(ctx, rule)
	if err != nil {
		return report, fmt.Errorf("query source %q: %w", rule.QueryEngine, err)
	}
	report.Queried = len(records)
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if opts.MaxRecords > 0 && len(records) > opts.MaxRecords {
		return report, fmt.Errorf("source %q returned %d records; runtime limit is %d", rule.QueryEngine, len(records), opts.MaxRecords)
	}
	var sourceBytes int64
	for i, record := range records {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		encoded, err := json.Marshal(record)
		if err != nil {
			return report, fmt.Errorf("measure source record %d: %w", i, err)
		}
		sourceBytes += int64(len(encoded))
		if opts.MaxBytes > 0 && sourceBytes > opts.MaxBytes {
			return report, fmt.Errorf("source %q returned more than %d encoded bytes", rule.QueryEngine, opts.MaxBytes)
		}
	}

	if excluder != nil {
		filtered := make([]model.Record, 0, len(records))
		for _, record := range records {
			if err := ctx.Err(); err != nil {
				return report, err
			}
			if excluder.IsExcluded(record) {
				report.Excluded++
				continue
			}
			filtered = append(filtered, record)
		}
		records = filtered
	}

	detectedAt := now().UTC()
	findings := make([]model.Finding, 0, len(records))
	occurrences := make(map[string]int, len(records))
	metadata := ruleMetadata(rule)
	for i, record := range records {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		payload, err := signal.BuildOutput(record, rule)
		if err != nil {
			return report, fmt.Errorf("build finding %d: %w", i, err)
		}
		baseID, err := model.FindingID(rule.UID, record)
		if err != nil {
			return report, fmt.Errorf("build finding %d ID: %w", i, err)
		}
		occurrence := occurrences[baseID]
		occurrences[baseID] = occurrence + 1
		id := baseID
		if occurrence > 0 {
			id, err = model.FindingIDForOccurrence(rule.UID, record, occurrence)
			if err != nil {
				return report, fmt.Errorf("build finding %d occurrence ID: %w", i, err)
			}
		}
		finding := model.Finding{
			SchemaVersion: model.FindingSchemaVersion, ID: id, RunID: runID,
			DetectedAt: detectedAt, Source: rule.QueryEngine,
			OutputFormat: string(rule.Output.Format), Rule: metadata, Payload: payload,
		}
		if sig, ok := payload.(*signal.Signal); ok {
			populateSignalFields(&finding, sig)
		}
		findings = append(findings, finding)
	}
	if err := measureFindings(ctx, findings, opts.MaxBytes); err != nil {
		return report, err
	}

	if rule.LLM != nil && rule.LLM.Enabled && len(findings) > 0 {
		var reviewErr error
		limit := reviewLimit(rule, len(findings))
		if rule.LLM.Required && limit != len(findings) {
			reviewErr = fmt.Errorf("required LLM review cannot cover all findings: maxFindings would cover %d of %d", limit, len(findings))
		} else if opts.Review == nil {
			reviewErr = fmt.Errorf("LLM reviewer is not configured")
		}
		var reviewInput []model.Finding
		if reviewErr == nil {
			reviewInput, reviewErr = cloneReviewInput(findings, limit)
		}
		if reviewErr == nil {
			var reviewCtx context.Context
			var cancel context.CancelFunc
			reviewCtx, cancel, reviewErr = boundedReviewContext(ctx, opts.ReviewTimeout)
			if reviewErr == nil {
				annotations, callErr := opts.Review(reviewCtx, reviewInput, rule)
				cancel()
				if callErr != nil {
					reviewErr = callErr
				} else {
					reviewErr = applyReviews(findings, reviewInput, annotations)
				}
			}
		}
		if reviewErr == nil {
			if err := measureFindings(ctx, findings, opts.MaxBytes); err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return report, ctxErr
				}
				for i := range findings {
					findings[i].Review = nil
				}
				reviewErr = fmt.Errorf("discard LLM review annotations: %w", err)
			}
		}
		if reviewErr != nil {
			report.ReviewError = reviewErr.Error()
		}
	}
	report.Findings = len(findings)
	if len(findings) == 0 {
		return report, nil
	}

	batch := model.PublishBatch{RunID: runID, DetectedAt: detectedAt, Source: rule.QueryEngine, Rule: metadata, Findings: findings}
	type sinkSpec struct {
		name     string
		required bool
	}
	specs := make([]sinkSpec, 0, len(rule.Publishers)+len(rule.BestEffortPublishers))
	for _, name := range rule.Publishers {
		specs = append(specs, sinkSpec{name: name, required: true})
	}
	for _, name := range rule.BestEffortPublishers {
		specs = append(specs, sinkSpec{name: name})
	}
	receipts := make([]model.SinkReceipt, len(specs))
	sinkErrors := make([]error, len(specs))
	var publishers sync.WaitGroup
	publishers.Add(len(specs))
	for i, spec := range specs {
		go func() {
			defer publishers.Done()
			startedSink := time.Now()
			receipt := model.SinkReceipt{Name: spec.name, Required: spec.required}
			publisher, err := registry.GetPublisher(spec.name)
			if err == nil {
				receipt.Attempted = len(findings)
				err = publisher.Publish(ctx, batch)
			}
			receipt.DurationMS = time.Since(startedSink).Milliseconds()
			if err != nil {
				receipt.Error = err.Error()
				sinkErrors[i] = fmt.Errorf("publish to %q: %w", spec.name, err)
			}
			receipts[i] = receipt
		}()
	}
	publishers.Wait()
	report.Sinks = receipts
	var terminalErrors []error
	if err := ctx.Err(); err != nil {
		terminalErrors = append(terminalErrors, err)
	}
	for i, spec := range specs {
		if spec.required && sinkErrors[i] != nil {
			terminalErrors = append(terminalErrors, sinkErrors[i])
		}
	}
	if report.ReviewError != "" && rule.LLM.Required {
		terminalErrors = append(terminalErrors, fmt.Errorf("required LLM review failed after deterministic findings were published: %s", report.ReviewError))
	}
	if err := errors.Join(terminalErrors...); err != nil {
		return report, err
	}
	return report, nil
}

func measureFindings(ctx context.Context, findings []model.Finding, maxBytes int64) error {
	var total int64
	for i, finding := range findings {
		if err := ctx.Err(); err != nil {
			return err
		}
		encoded, err := json.Marshal(finding)
		if err != nil {
			return fmt.Errorf("measure finding %d: %w", i, err)
		}
		total += int64(len(encoded))
		if maxBytes > 0 && total > maxBytes {
			return fmt.Errorf("findings exceed runtime encoded byte limit %d", maxBytes)
		}
	}
	return ctx.Err()
}

// ValidateRuleFiles resolves and parses local files referenced by a rule
// without querying a source.
func ValidateRuleFiles(rulePath string, rule *config.RuleConfig) error {
	if rule == nil {
		return fmt.Errorf("rule is nil")
	}
	if _, err := loadExcluder(rulePath, rule); err != nil {
		return err
	}
	if rule.QueryEngine == "file.ndjson" {
		if err := stdio.ValidateFile(rule.Query); err != nil {
			return fmt.Errorf("validate file.ndjson source: %w", err)
		}
	}
	return nil
}

func loadExcluder(rulePath string, rule *config.RuleConfig) (*exclusion.Excluder, error) {
	if rule == nil || rule.ExclusionsPath == "" {
		return nil, nil
	}
	path, err := resolveRulePath(rulePath, rule.ExclusionsPath)
	if err != nil {
		return nil, err
	}
	excluder, err := exclusion.NewExcluder(path)
	if err != nil {
		return nil, fmt.Errorf("load exclusions: %w", err)
	}
	return excluder, nil
}

func reviewLimit(rule *config.RuleConfig, findingCount int) int {
	limit := defaultReviewLimit
	if rule.LLM != nil && rule.LLM.MaxFindings > 0 {
		limit = rule.LLM.MaxFindings
	}
	if limit > findingCount {
		return findingCount
	}
	return limit
}

func cloneReviewInput(findings []model.Finding, limit int) ([]model.Finding, error) {
	encoded, err := json.Marshal(findings[:limit])
	if err != nil {
		return nil, fmt.Errorf("clone LLM review input: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var cloned []model.Finding
	if err := decoder.Decode(&cloned); err != nil {
		return nil, fmt.Errorf("clone LLM review input: %w", err)
	}
	return cloned, nil
}

func boundedReviewContext(parent context.Context, requested time.Duration) (context.Context, context.CancelFunc, error) {
	if requested <= 0 {
		requested = defaultReviewTimeout
	}
	if deadline, ok := parent.Deadline(); ok {
		available := time.Until(deadline) - minimumPublishReserve
		if available <= 0 {
			return nil, nil, fmt.Errorf("skip LLM review: less than %s remains for required publishers", minimumPublishReserve)
		}
		if requested > available {
			requested = available
		}
	}
	ctx, cancel := context.WithTimeout(parent, requested)
	return ctx, cancel, nil
}

func applyReviews(findings, requested []model.Finding, annotations map[string]model.Review) error {
	expected := make(map[string]struct{}, len(requested))
	for _, finding := range requested {
		expected[finding.ID] = struct{}{}
	}
	for findingID, review := range annotations {
		if _, ok := expected[findingID]; !ok {
			return fmt.Errorf("LLM reviewer returned annotation for unknown finding_id %q", findingID)
		}
		switch review.Verdict {
		case "suspicious", "benign", "uncertain":
		default:
			return fmt.Errorf("LLM reviewer returned invalid verdict %q for finding_id %q", review.Verdict, findingID)
		}
		if strings.TrimSpace(review.Reason) == "" || strings.TrimSpace(review.Reviewer) == "" || review.ReviewedAt == "" {
			return fmt.Errorf("LLM reviewer returned incomplete annotation for finding_id %q", findingID)
		}
		if _, err := time.Parse(time.RFC3339Nano, review.ReviewedAt); err != nil {
			return fmt.Errorf("LLM reviewer returned invalid reviewed_at for finding_id %q", findingID)
		}
	}
	for findingID := range expected {
		if _, ok := annotations[findingID]; !ok {
			return fmt.Errorf("LLM reviewer omitted annotation for finding_id %q", findingID)
		}
	}
	for i := range findings {
		if review, ok := annotations[findings[i].ID]; ok {
			copy := review
			findings[i].Review = &copy
		}
	}
	return nil
}

func ruleMetadata(rule *config.RuleConfig) model.RuleMetadata {
	ttpIDs := make([]string, 0, len(rule.TTPs))
	for _, ttp := range rule.TTPs {
		if ttp.ID != "" {
			ttpIDs = append(ttpIDs, ttp.ID)
		}
	}
	return model.RuleMetadata{
		ID: rule.UID, Name: rule.Name, Status: rule.Status,
		Confidence: string(rule.Confidence), Tags: append([]string(nil), rule.Tags...), TTPIDs: ttpIDs,
	}
}

func populateSignalFields(finding *model.Finding, sig *signal.Signal) {
	if !sig.Timestamp.IsZero() {
		timestamp := sig.Timestamp.UTC()
		finding.EventAt = &timestamp
	}
	finding.Attributes = model.FindingAttributes{
		ActorUserName: sig.Actor.User.Name, ActorUserUID: sig.Actor.User.UID,
		ResourceName: sig.Resource.Name, ResourceType: sig.Resource.Type, ResourceUID: sig.Resource.UID,
		SrcHostname: sig.SrcEndpoint.Hostname, SrcIP: sig.SrcEndpoint.IP,
		DstHostname: sig.DstEndpoint.Hostname, DstIP: sig.DstEndpoint.IP,
		Message: sig.Message, EventID: sig.Metadata.EventID, EventIndex: sig.Metadata.EventIndex,
	}
}

func resolveRulePath(rulePath, referenced string) (string, error) {
	if filepath.IsAbs(referenced) {
		if _, err := os.Stat(referenced); err != nil {
			return "", fmt.Errorf("resolve exclusions path %q: %w", referenced, err)
		}
		return referenced, nil
	}
	if rulePath != "" {
		candidate := filepath.Join(filepath.Dir(rulePath), referenced)
		if _, err := os.Stat(candidate); err != nil {
			return "", fmt.Errorf("resolve exclusions path %q relative to rule %q: %w", referenced, rulePath, err)
		}
		return candidate, nil
	}
	if _, err := os.Stat(referenced); err != nil {
		return "", fmt.Errorf("resolve exclusions path %q: %w", referenced, err)
	}
	return referenced, nil
}
