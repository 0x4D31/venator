package clickhouse

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/0x4D31/venator/internal/model"
)

var findingColumns = []string{
	"detected_at",
	"event_at",
	"run_id",
	"finding_id",
	"rule_id",
	"rule_name",
	"rule_status",
	"confidence",
	"source",
	"output_format",
	"actor_user_name",
	"actor_user_uid",
	"resource_name",
	"resource_type",
	"resource_uid",
	"src_hostname",
	"src_ip",
	"dst_hostname",
	"dst_ip",
	"message",
	"event_id",
	"event_index",
	"tags",
	"ttp_ids",
	"payload",
}

// Sink inserts the canonical Finding envelope into a queryable scalar schema,
// retaining the complete JSON envelope in payload for forward compatibility.
type Sink struct {
	client      *Client
	insertQuery string
}

func NewSink(client *Client) (*Sink, error) {
	if err := client.ensureOpen(); err != nil {
		return nil, fmt.Errorf("create ClickHouse sink: %w", err)
	}
	if client.cfg.Sink == nil {
		return nil, errors.New("create ClickHouse sink: sink is not configured")
	}
	table, err := quoteTableIdentifier(client.cfg.Sink.Table)
	if err != nil {
		return nil, fmt.Errorf("create ClickHouse sink: %w", err)
	}
	columns := make([]string, len(findingColumns))
	for i, column := range findingColumns {
		columns[i] = "`" + column + "`"
	}
	return &Sink{
		client:      client,
		insertQuery: "INSERT INTO " + table + " (" + strings.Join(columns, ", ") + ")",
	}, nil
}

func quoteTableIdentifier(table string) (string, error) {
	if table == "" || strings.TrimSpace(table) != table {
		return "", errors.New("table must be a non-empty identifier without surrounding whitespace")
	}
	parts := strings.Split(table, ".")
	if len(parts) > 2 {
		return "", errors.New("table must be table or database.table")
	}
	for i, part := range parts {
		if !safeIdentifier(part) {
			return "", fmt.Errorf("identifier component %q contains unsupported characters", part)
		}
		parts[i] = "`" + part + "`"
	}
	return strings.Join(parts, "."), nil
}

func safeIdentifier(identifier string) bool {
	if identifier == "" {
		return false
	}
	for i := 0; i < len(identifier); i++ {
		character := identifier[i]
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') || character == '_' ||
			(i > 0 && character >= '0' && character <= '9') {
			continue
		}
		return false
	}
	return true
}

type findingRow struct {
	detectedAt   time.Time
	eventAt      *time.Time
	runID        string
	findingID    string
	ruleID       string
	ruleName     string
	ruleStatus   string
	confidence   string
	source       string
	outputFormat string
	attributes   model.FindingAttributes
	tags         []string
	ttpIDs       []string
	payload      string
}

func newFindingRow(batch model.PublishBatch, finding model.Finding) (findingRow, error) {
	if finding.DetectedAt.IsZero() {
		return findingRow{}, errors.New("detected_at is required")
	}
	if finding.RunID == "" {
		return findingRow{}, errors.New("run_id is required")
	}
	if batch.RunID != "" && batch.RunID != finding.RunID {
		return findingRow{}, fmt.Errorf("finding run_id %q does not match batch run_id %q", finding.RunID, batch.RunID)
	}
	if finding.Rule.ID == "" {
		return findingRow{}, errors.New("rule_id is required")
	}
	if len(finding.ID) != 64 {
		return findingRow{}, fmt.Errorf("finding_id must be 64 hexadecimal characters, got %d", len(finding.ID))
	}
	if _, err := hex.DecodeString(finding.ID); err != nil {
		return findingRow{}, fmt.Errorf("finding_id is not hexadecimal: %w", err)
	}
	payload, err := json.Marshal(finding)
	if err != nil {
		return findingRow{}, fmt.Errorf("marshal canonical finding: %w", err)
	}

	detectedAt := finding.DetectedAt.UTC()
	var eventAt *time.Time
	if finding.EventAt != nil {
		eventAtUTC := finding.EventAt.UTC()
		eventAt = &eventAtUTC
	}
	return findingRow{
		detectedAt:   detectedAt,
		eventAt:      eventAt,
		runID:        finding.RunID,
		findingID:    finding.ID,
		ruleID:       finding.Rule.ID,
		ruleName:     finding.Rule.Name,
		ruleStatus:   finding.Rule.Status,
		confidence:   finding.Rule.Confidence,
		source:       finding.Source,
		outputFormat: finding.OutputFormat,
		attributes:   finding.Attributes,
		tags:         nonNilStrings(finding.Rule.Tags),
		ttpIDs:       nonNilStrings(finding.Rule.TTPIDs),
		payload:      string(payload),
	}, nil
}

func nonNilStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	return append([]string(nil), values...)
}

func (r findingRow) values() []any {
	return []any{
		r.detectedAt,
		r.eventAt,
		r.runID,
		r.findingID,
		r.ruleID,
		r.ruleName,
		r.ruleStatus,
		r.confidence,
		r.source,
		r.outputFormat,
		r.attributes.ActorUserName,
		r.attributes.ActorUserUID,
		r.attributes.ResourceName,
		r.attributes.ResourceType,
		r.attributes.ResourceUID,
		r.attributes.SrcHostname,
		r.attributes.SrcIP,
		r.attributes.DstHostname,
		r.attributes.DstIP,
		r.attributes.Message,
		r.attributes.EventID,
		r.attributes.EventIndex,
		r.tags,
		r.ttpIDs,
		r.payload,
	}
}

func (s *Sink) Publish(ctx context.Context, batch model.PublishBatch) (err error) {
	if len(batch.Findings) == 0 {
		return nil
	}
	if s == nil || s.client == nil {
		return errors.New("publish ClickHouse findings: sink is not initialized")
	}
	if err := s.client.ensureOpen(); err != nil {
		return fmt.Errorf("publish ClickHouse findings: %w", err)
	}

	rows := make([]findingRow, len(batch.Findings))
	for i, finding := range batch.Findings {
		if err := ctx.Err(); err != nil {
			return err
		}
		row, rowErr := newFindingRow(batch, finding)
		if rowErr != nil {
			return fmt.Errorf("prepare ClickHouse finding %d: %w", i, rowErr)
		}
		rows[i] = row
	}

	prepared, err := s.client.conn.PrepareBatch(ctx, s.insertQuery)
	if err != nil {
		return fmt.Errorf("prepare ClickHouse insert batch: %w", err)
	}
	if prepared == nil {
		return errors.New("prepare ClickHouse insert batch: driver returned a nil batch")
	}
	defer func() {
		err = errors.Join(err, wrapError("close ClickHouse insert batch", prepared.Close()))
	}()
	for i, row := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := prepared.Append(row.values()...); err != nil {
			return fmt.Errorf("append ClickHouse finding %d (%s): %w", i, row.findingID, err)
		}
	}
	if err := prepared.Send(); err != nil {
		return fmt.Errorf("send ClickHouse insert batch: %w", err)
	}
	return nil
}

func (s *Sink) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Close()
}
