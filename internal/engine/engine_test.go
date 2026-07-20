package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/0x4D31/venator/connector"
	"github.com/0x4D31/venator/internal/config"
	"github.com/0x4D31/venator/internal/model"
)

type fakeSource struct {
	records []model.Record
	err     error
}

func (s fakeSource) Query(context.Context, *config.RuleConfig) ([]model.Record, error) {
	return s.records, s.err
}

type fakeSink struct {
	batches *[]model.PublishBatch
	err     error
}

type blockingSink struct{}

func (blockingSink) Publish(ctx context.Context, _ model.PublishBatch) error {
	<-ctx.Done()
	return ctx.Err()
}

type cancelingSource struct {
	cancel context.CancelFunc
}

func (s cancelingSource) Query(context.Context, *config.RuleConfig) ([]model.Record, error) {
	s.cancel()
	return []model.Record{{"x": 1}}, nil
}

func (s fakeSink) Publish(_ context.Context, batch model.PublishBatch) error {
	*s.batches = append(*s.batches, batch)
	return s.err
}

type fakeRegistry struct {
	sources map[string]connector.QueryRunner
	sinks   map[string]connector.Publisher
}

func (r fakeRegistry) GetQueryRunner(name string) (connector.QueryRunner, error) {
	source, ok := r.sources[name]
	if !ok {
		return nil, errors.New("missing source")
	}
	return source, nil
}

func (r fakeRegistry) GetPublisher(name string) (connector.Publisher, error) {
	sink, ok := r.sinks[name]
	if !ok {
		return nil, errors.New("missing sink")
	}
	return sink, nil
}

func testRule() *config.RuleConfig {
	return &config.RuleConfig{
		Name: "test", UID: "rule-1", Status: "stable", Confidence: config.ConfidenceHigh,
		Enabled: true, QueryEngine: "fake", Language: "SQL", Query: "select 1",
		Publishers: []string{"required"}, Output: config.Output{Format: config.OutputFormatRaw},
	}
}

func TestRunReturnsErrorWhenRequiredSinkFails(t *testing.T) {
	var batches []model.PublishBatch
	registry := fakeRegistry{
		sources: map[string]connector.QueryRunner{"fake": fakeSource{records: []model.Record{{"x": 1}}}},
		sinks:   map[string]connector.Publisher{"required": fakeSink{batches: &batches, err: errors.New("down")}},
	}
	report, err := Run(context.Background(), registry, testRule(), Options{Now: fixedClock()})
	if err == nil {
		t.Fatal("expected sink error")
	}
	if report.Status != "failed" {
		t.Fatalf("status = %q", report.Status)
	}
	if len(batches) != 1 {
		t.Fatalf("batches = %d", len(batches))
	}
}

func TestRunAttemptsBestEffortSinkWithoutFailingRun(t *testing.T) {
	var required, optional []model.PublishBatch
	rule := testRule()
	rule.BestEffortPublishers = []string{"optional"}
	registry := fakeRegistry{
		sources: map[string]connector.QueryRunner{"fake": fakeSource{records: []model.Record{{"x": 1}}}},
		sinks: map[string]connector.Publisher{
			"required": fakeSink{batches: &required},
			"optional": fakeSink{batches: &optional, err: errors.New("down")},
		},
	}
	report, err := Run(context.Background(), registry, rule, Options{Now: fixedClock()})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "succeeded" {
		t.Fatalf("status = %q", report.Status)
	}
	if report.Sinks[1].Error == "" {
		t.Fatal("expected optional sink error in receipt")
	}
}

func TestRequiredSinksFanOutWithoutDeadlineStarvation(t *testing.T) {
	var healthy []model.PublishBatch
	rule := testRule()
	rule.Publishers = []string{"blocked", "healthy"}
	registry := fakeRegistry{
		sources: map[string]connector.QueryRunner{"fake": fakeSource{records: []model.Record{{"x": 1}}}},
		sinks: map[string]connector.Publisher{
			"blocked": blockingSink{},
			"healthy": fakeSink{batches: &healthy},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	report, err := Run(ctx, registry, rule, Options{Now: fixedClock()})
	if err == nil || len(healthy) != 1 {
		t.Fatalf("report/healthy/error = %#v %#v %v", report, healthy, err)
	}
	if len(report.Sinks) != 2 || report.Sinks[0].Name != "blocked" || report.Sinks[1].Name != "healthy" {
		t.Fatalf("receipts lost configured ordering: %#v", report.Sinks)
	}
}

func TestRunFailsWhenBestEffortSinkExhaustsContext(t *testing.T) {
	var healthy []model.PublishBatch
	rule := testRule()
	rule.BestEffortPublishers = []string{"blocked"}
	registry := fakeRegistry{
		sources: map[string]connector.QueryRunner{"fake": fakeSource{records: []model.Record{{"x": 1}}}},
		sinks: map[string]connector.Publisher{
			"required": fakeSink{batches: &healthy},
			"blocked":  blockingSink{},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	report, err := Run(ctx, registry, rule, Options{Now: fixedClock()})
	if !errors.Is(err, context.DeadlineExceeded) || report.Status != "failed" || len(healthy) != 1 {
		t.Fatalf("report/healthy/error = %#v %#v %v", report, healthy, err)
	}
}

func TestRunFailsWhenSourceReturnsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	registry := fakeRegistry{sources: map[string]connector.QueryRunner{"fake": cancelingSource{cancel: cancel}}}
	report, err := Run(ctx, registry, testRule(), Options{Now: fixedClock()})
	if !errors.Is(err, context.Canceled) || report.Status != "failed" || report.Queried != 1 {
		t.Fatalf("report/error = %#v %v", report, err)
	}
}

func TestRunSkipsDisabledRule(t *testing.T) {
	rule := testRule()
	rule.Enabled = false
	report, err := Run(context.Background(), fakeRegistry{}, rule, Options{Now: fixedClock()})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "skipped" || report.Queried != 0 {
		t.Fatalf("report = %#v", report)
	}
}

func TestRunDoesNotSkipPreCanceledDisabledRule(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rule := testRule()
	rule.Enabled = false
	report, err := Run(ctx, fakeRegistry{}, rule, Options{Now: fixedClock()})
	if !errors.Is(err, context.Canceled) || report.Status != "failed" || report.Queried != 0 {
		t.Fatalf("report/error = %#v %v", report, err)
	}
}

func TestRunEnforcesRuntimeRecordLimitBeforePublishing(t *testing.T) {
	var batches []model.PublishBatch
	registry := fakeRegistry{
		sources: map[string]connector.QueryRunner{"fake": fakeSource{records: []model.Record{{"x": 1}, {"x": 2}}}},
		sinks:   map[string]connector.Publisher{"required": fakeSink{batches: &batches}},
	}
	report, err := Run(context.Background(), registry, testRule(), Options{Now: fixedClock(), MaxRecords: 1})
	if err == nil || !strings.Contains(err.Error(), "runtime limit") {
		t.Fatalf("error = %v", err)
	}
	if report.Queried != 2 || len(batches) != 0 {
		t.Fatalf("report/batches = %#v %#v", report, batches)
	}
}

func TestRunEnforcesRuntimeByteLimitBeforePublishing(t *testing.T) {
	var batches []model.PublishBatch
	registry := fakeRegistry{
		sources: map[string]connector.QueryRunner{"fake": fakeSource{records: []model.Record{{"message": "larger than cap"}}}},
		sinks:   map[string]connector.Publisher{"required": fakeSink{batches: &batches}},
	}
	report, err := Run(context.Background(), registry, testRule(), Options{Now: fixedClock(), MaxBytes: 5})
	if err == nil || !strings.Contains(err.Error(), "encoded bytes") {
		t.Fatalf("error = %v", err)
	}
	if report.Queried != 1 || len(batches) != 0 {
		t.Fatalf("report/batches = %#v %#v", report, batches)
	}
}

func TestOversizedReviewCannotSuppressDeterministicFinding(t *testing.T) {
	tests := []struct {
		name     string
		required bool
	}{
		{name: "optional"},
		{name: "required", required: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var batches []model.PublishBatch
			registry := fakeRegistry{
				sources: map[string]connector.QueryRunner{"fake": fakeSource{records: []model.Record{{"x": 1}}}},
				sinks:   map[string]connector.Publisher{"required": fakeSink{batches: &batches}},
			}
			rule := testRule()
			rule.LLM = &config.LLM{Enabled: true, Required: test.required, Prompt: "review"}
			report, err := Run(context.Background(), registry, rule, Options{
				Now: fixedClock(), MaxBytes: 1024,
				Review: func(_ context.Context, findings []model.Finding, _ *config.RuleConfig) (map[string]model.Review, error) {
					return map[string]model.Review{findings[0].ID: {
						Verdict: "uncertain", Reason: strings.Repeat("x", 2048), Reviewer: "test/model", ReviewedAt: "2026-07-18T12:00:00Z",
					}}, nil
				},
			})
			if test.required && (err == nil || !strings.Contains(err.Error(), "required LLM review failed")) {
				t.Fatalf("required review error = %v", err)
			}
			if !test.required && err != nil {
				t.Fatalf("optional review error = %v", err)
			}
			if report.ReviewError == "" || !strings.Contains(report.ReviewError, "encoded byte limit") {
				t.Fatalf("report = %#v", report)
			}
			if len(batches) != 1 || len(batches[0].Findings) != 1 || batches[0].Findings[0].Review != nil {
				t.Fatalf("deterministic finding was not published without review: %#v", batches)
			}
		})
	}
}

func TestRunRetainsIdenticalSourceRowsWithDistinctStableIDs(t *testing.T) {
	var batches []model.PublishBatch
	records := []model.Record{{"event": "same"}, {"event": "same"}}
	registry := fakeRegistry{
		sources: map[string]connector.QueryRunner{"fake": fakeSource{records: records}},
		sinks:   map[string]connector.Publisher{"required": fakeSink{batches: &batches}},
	}
	report, err := Run(context.Background(), registry, testRule(), Options{Now: fixedClock()})
	if err != nil {
		t.Fatal(err)
	}
	if report.Findings != 2 || len(batches) != 1 || len(batches[0].Findings) != 2 {
		t.Fatalf("report/batches = %#v %#v", report, batches)
	}
	first := batches[0].Findings[0].ID
	second := batches[0].Findings[1].ID
	if first == second {
		t.Fatalf("duplicate rows received the same finding ID %q", first)
	}
}

func TestRunUsesConfiguredIdentityFieldsForStableIDs(t *testing.T) {
	rule := testRule()
	rule.Identity = &config.Identity{Fields: []string{"record_id"}}

	run := func(record model.Record) string {
		t.Helper()
		var batches []model.PublishBatch
		registry := fakeRegistry{
			sources: map[string]connector.QueryRunner{"fake": fakeSource{records: []model.Record{record}}},
			sinks:   map[string]connector.Publisher{"required": fakeSink{batches: &batches}},
		}
		if _, err := Run(context.Background(), registry, rule, Options{Now: fixedClock()}); err != nil {
			t.Fatal(err)
		}
		return batches[0].Findings[0].ID
	}

	first := run(model.Record{"record_id": "finding-1", "run_id": "run-1"})
	second := run(model.Record{"record_id": "finding-1", "run_id": "run-2"})
	if first != second {
		t.Fatalf("finding ID changed with non-identity field: %q != %q", first, second)
	}
}

func TestRunTreatsIdentityFieldsAsExactTopLevelKeys(t *testing.T) {
	var batches []model.PublishBatch
	rule := testRule()
	rule.Identity = &config.Identity{Fields: []string{"event.id"}}
	registry := fakeRegistry{
		sources: map[string]connector.QueryRunner{"fake": fakeSource{records: []model.Record{{
			"event.id": "top-level",
			"event":    map[string]any{"id": "nested"},
		}}}},
		sinks: map[string]connector.Publisher{"required": fakeSink{batches: &batches}},
	}
	if _, err := Run(context.Background(), registry, rule, Options{Now: fixedClock()}); err != nil {
		t.Fatal(err)
	}
	want, err := model.FindingID(rule.UID, model.Record{"event.id": "top-level"})
	if err != nil {
		t.Fatal(err)
	}
	if got := batches[0].Findings[0].ID; got != want {
		t.Fatalf("finding ID = %q, want %q", got, want)
	}
}

func TestRunRejectsMissingIdentityFieldBeforePublishing(t *testing.T) {
	var batches []model.PublishBatch
	rule := testRule()
	rule.Identity = &config.Identity{Fields: []string{"record_id"}}
	registry := fakeRegistry{
		sources: map[string]connector.QueryRunner{"fake": fakeSource{records: []model.Record{{"message": "missing identity"}}}},
		sinks:   map[string]connector.Publisher{"required": fakeSink{batches: &batches}},
	}
	report, err := Run(context.Background(), registry, rule, Options{Now: fixedClock()})
	if err == nil || !strings.Contains(err.Error(), `field "record_id" is missing`) {
		t.Fatalf("error = %v", err)
	}
	if report.Status != "failed" || len(batches) != 0 {
		t.Fatalf("report/batches = %#v %#v", report, batches)
	}
}

func TestRunRejectsIdentityCollisionBeforePublishing(t *testing.T) {
	var batches []model.PublishBatch
	rule := testRule()
	rule.Identity = &config.Identity{Fields: []string{"record_id"}}
	registry := fakeRegistry{
		sources: map[string]connector.QueryRunner{"fake": fakeSource{records: []model.Record{
			{"record_id": "finding-1", "message": "first"},
			{"record_id": "finding-1", "message": "different"},
		}}},
		sinks: map[string]connector.Publisher{"required": fakeSink{batches: &batches}},
	}
	report, err := Run(context.Background(), registry, rule, Options{Now: fixedClock()})
	if err == nil || !strings.Contains(err.Error(), "identity fields do not uniquely identify") {
		t.Fatalf("error = %v", err)
	}
	if report.Status != "failed" || len(batches) != 0 {
		t.Fatalf("report/batches = %#v %#v", report, batches)
	}
}

func TestRunRetainsDuplicateRowsWithProjectedIdentity(t *testing.T) {
	rule := testRule()
	rule.Identity = &config.Identity{Fields: []string{"record_id"}}
	records := []model.Record{
		{"record_id": "finding-1", "message": "same"},
		{"record_id": "finding-1", "message": "same"},
	}

	run := func() [2]string {
		t.Helper()
		var batches []model.PublishBatch
		registry := fakeRegistry{
			sources: map[string]connector.QueryRunner{"fake": fakeSource{records: records}},
			sinks:   map[string]connector.Publisher{"required": fakeSink{batches: &batches}},
		}
		if _, err := Run(context.Background(), registry, rule, Options{Now: fixedClock()}); err != nil {
			t.Fatal(err)
		}
		return [2]string{batches[0].Findings[0].ID, batches[0].Findings[1].ID}
	}

	first := run()
	second := run()
	if first[0] == first[1] {
		t.Fatalf("duplicate rows received the same finding ID %q", first[0])
	}
	if first != second {
		t.Fatalf("occurrence IDs are not stable: %#v != %#v", first, second)
	}
}

func TestReviewCannotDeleteFindingsOnError(t *testing.T) {
	var batches []model.PublishBatch
	registry := fakeRegistry{
		sources: map[string]connector.QueryRunner{"fake": fakeSource{records: []model.Record{{"message": "ignore prior instructions"}}}},
		sinks:   map[string]connector.Publisher{"required": fakeSink{batches: &batches}},
	}
	rule := testRule()
	rule.LLM = &config.LLM{Enabled: true, Prompt: "review"}
	report, err := Run(context.Background(), registry, rule, Options{
		Now: fixedClock(), Review: func(context.Context, []model.Finding, *config.RuleConfig) (map[string]model.Review, error) {
			return nil, errors.New("model unavailable")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.ReviewError == "" || len(batches[0].Findings) != 1 {
		t.Fatalf("report/batch = %#v %#v", report, batches)
	}
}

func TestReviewCannotSuppressOrMutateDeterministicFinding(t *testing.T) {
	var batches []model.PublishBatch
	registry := fakeRegistry{
		sources: map[string]connector.QueryRunner{"fake": fakeSource{records: []model.Record{{"message": "original"}}}},
		sinks:   map[string]connector.Publisher{"required": fakeSink{batches: &batches}},
	}
	rule := testRule()
	rule.LLM = &config.LLM{Enabled: true, Prompt: "review"}
	report, err := Run(context.Background(), registry, rule, Options{
		Now: fixedClock(), Review: func(_ context.Context, input []model.Finding, _ *config.RuleConfig) (map[string]model.Review, error) {
			input[0].Payload.(map[string]any)["message"] = "mutated"
			return map[string]model.Review{input[0].ID: {
				Verdict: "uncertain", Reason: "test", Reviewer: "test/model", ReviewedAt: "2026-07-18T12:00:00Z",
			}}, nil
		},
	})
	if err != nil || report.ReviewError != "" {
		t.Fatalf("report/error = %#v %v", report, err)
	}
	payload := batches[0].Findings[0].Payload.(model.Record)
	if payload["message"] != "original" || batches[0].Findings[0].Review == nil {
		t.Fatalf("finding = %#v", batches[0].Findings[0])
	}
}

func TestIncompleteReviewIsBestEffortAndCannotSuppressPublishing(t *testing.T) {
	var batches []model.PublishBatch
	registry := fakeRegistry{
		sources: map[string]connector.QueryRunner{"fake": fakeSource{records: []model.Record{{"x": 1}}}},
		sinks:   map[string]connector.Publisher{"required": fakeSink{batches: &batches}},
	}
	rule := testRule()
	rule.LLM = &config.LLM{Enabled: true, Prompt: "review"}
	report, err := Run(context.Background(), registry, rule, Options{
		Now: fixedClock(), Review: func(context.Context, []model.Finding, *config.RuleConfig) (map[string]model.Review, error) {
			return map[string]model.Review{}, nil
		},
	})
	if err != nil || report.ReviewError == "" || len(batches) != 1 || len(batches[0].Findings) != 1 {
		t.Fatalf("report/batches/error = %#v %#v %v", report, batches, err)
	}
}

func TestMalformedReviewIsDiscardedBeforePublishing(t *testing.T) {
	var batches []model.PublishBatch
	registry := fakeRegistry{
		sources: map[string]connector.QueryRunner{"fake": fakeSource{records: []model.Record{{"x": 1}}}},
		sinks:   map[string]connector.Publisher{"required": fakeSink{batches: &batches}},
	}
	rule := testRule()
	rule.LLM = &config.LLM{Enabled: true, Prompt: "review"}
	report, err := Run(context.Background(), registry, rule, Options{
		Now: fixedClock(), Review: func(_ context.Context, findings []model.Finding, _ *config.RuleConfig) (map[string]model.Review, error) {
			return map[string]model.Review{findings[0].ID: {
				Verdict: "uncertain", Reason: "reason", Reviewer: "test/model", ReviewedAt: "not-a-time",
			}}, nil
		},
	})
	if err != nil || !strings.Contains(report.ReviewError, "invalid reviewed_at") || len(batches) != 1 || batches[0].Findings[0].Review != nil {
		t.Fatalf("report/batches/error = %#v %#v %v", report, batches, err)
	}
}

func TestOptionalReviewTimeoutPreservesPublisherBudget(t *testing.T) {
	var batches []model.PublishBatch
	registry := fakeRegistry{
		sources: map[string]connector.QueryRunner{"fake": fakeSource{records: []model.Record{{"x": 1}}}},
		sinks:   map[string]connector.Publisher{"required": fakeSink{batches: &batches}},
	}
	rule := testRule()
	rule.LLM = &config.LLM{Enabled: true, Prompt: "review"}
	report, err := Run(context.Background(), registry, rule, Options{
		Now: fixedClock(), ReviewTimeout: 20 * time.Millisecond,
		Review: func(ctx context.Context, _ []model.Finding, _ *config.RuleConfig) (map[string]model.Review, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	if err != nil || report.ReviewError == "" || len(batches) != 1 {
		t.Fatalf("report/batches/error = %#v %#v %v", report, batches, err)
	}
}

func TestRequiredReviewFailsAfterPublishingWhenReviewerMissing(t *testing.T) {
	var batches []model.PublishBatch
	registry := fakeRegistry{
		sources: map[string]connector.QueryRunner{"fake": fakeSource{records: []model.Record{{"x": 1}}}},
		sinks:   map[string]connector.Publisher{"required": fakeSink{batches: &batches}},
	}
	rule := testRule()
	rule.LLM = &config.LLM{Enabled: true, Required: true, Prompt: "review"}
	report, err := Run(context.Background(), registry, rule, Options{Now: fixedClock()})
	if err == nil || !strings.Contains(err.Error(), "required LLM review failed") {
		t.Fatalf("error = %v", err)
	}
	if len(batches) != 1 || report.ReviewError == "" {
		t.Fatalf("report/batches = %#v %#v", report, batches)
	}
}

func TestRequiredReviewCapFailsBeforeCallingReviewer(t *testing.T) {
	var batches []model.PublishBatch
	registry := fakeRegistry{
		sources: map[string]connector.QueryRunner{"fake": fakeSource{records: []model.Record{{"x": 1}, {"x": 2}}}},
		sinks:   map[string]connector.Publisher{"required": fakeSink{batches: &batches}},
	}
	rule := testRule()
	rule.LLM = &config.LLM{Enabled: true, Required: true, Prompt: "review", MaxFindings: 1}
	calls := 0
	report, err := Run(context.Background(), registry, rule, Options{
		Now: fixedClock(), Review: func(context.Context, []model.Finding, *config.RuleConfig) (map[string]model.Review, error) {
			calls++
			return nil, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "required LLM review failed") || report.ReviewError == "" {
		t.Fatalf("report/error = %#v %v", report, err)
	}
	if calls != 0 || len(batches) != 1 || len(batches[0].Findings) != 2 {
		t.Fatalf("calls/batches = %d %#v", calls, batches)
	}
}

func TestSinkReceiptDoesNotClaimAttemptWhenPublisherLookupFails(t *testing.T) {
	registry := fakeRegistry{sources: map[string]connector.QueryRunner{
		"fake": fakeSource{records: []model.Record{{"x": 1}}},
	}}
	report, err := Run(context.Background(), registry, testRule(), Options{Now: fixedClock()})
	if err == nil || len(report.Sinks) != 1 {
		t.Fatalf("report/error = %#v %v", report, err)
	}
	if report.Sinks[0].Attempted != 0 || report.Sinks[0].Error == "" {
		t.Fatalf("receipt = %#v", report.Sinks[0])
	}
}

func fixedClock() func() time.Time {
	current := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	return func() time.Time { current = current.Add(time.Millisecond); return current }
}
