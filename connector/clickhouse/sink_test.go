package clickhouse

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/0x4D31/venator/internal/model"
)

func TestQuoteTableIdentifier(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{input: "findings", want: "`findings`", ok: true},
		{input: "security.venator_findings", want: "`security`.`venator_findings`", ok: true},
		{input: "9findings", ok: false},
		{input: "security..findings", ok: false},
		{input: "security.findings.extra", ok: false},
		{input: "findings;DROP TABLE users", ok: false},
		{input: " findings", ok: false},
		{input: "`findings`", ok: false},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			got, err := quoteTableIdentifier(test.input)
			if test.ok && err != nil {
				t.Fatalf("quoteTableIdentifier() error = %v", err)
			}
			if !test.ok && err == nil {
				t.Fatalf("quoteTableIdentifier() = %q, want error", got)
			}
			if got != test.want {
				t.Errorf("quoteTableIdentifier() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSinkPublishInsertsCanonicalFinding(t *testing.T) {
	prepared := &fakeBatch{}
	var insertQuery string
	conn := &fakeConnection{prepareBatchFn: func(_ context.Context, query string) (writeBatch, error) {
		insertQuery = query
		return prepared, nil
	}}
	cfg := baseConfig()
	cfg.Sink.Table = "security.venator_findings"
	sink, err := NewSink(clientForTest(t, cfg, conn))
	if err != nil {
		t.Fatal(err)
	}
	detectedAt := time.Date(2026, 7, 18, 1, 2, 3, 4, time.FixedZone("offset", -5*60*60))
	eventAt := detectedAt.Add(-time.Minute)
	finding := model.Finding{
		SchemaVersion: model.FindingSchemaVersion,
		ID:            strings.Repeat("ab", 32),
		RunID:         "run-1",
		DetectedAt:    detectedAt,
		EventAt:       &eventAt,
		Source:        "clickhouse.home",
		OutputFormat:  "signal",
		Rule: model.RuleMetadata{
			ID:         "rule-1",
			Name:       "Suspicious login",
			Status:     "stable",
			Confidence: "high",
			Tags:       []string{"auth", "local"},
			TTPIDs:     []string{"T1078"},
		},
		Attributes: model.FindingAttributes{
			ActorUserName: "alice",
			SrcIP:         "192.0.2.1",
			Message:       "multiple failed logins",
			EventID:       "event-9",
		},
		Payload: model.Record{"attempts": uint64(7)},
		Review: &model.Review{
			Verdict:    "suspicious",
			Reason:     "unusual source",
			Reviewer:   "local-model",
			ReviewedAt: "2026-07-18T06:03:00Z",
		},
	}

	err = sink.Publish(context.Background(), model.PublishBatch{
		RunID:    "run-1",
		Findings: []model.Finding{finding},
	})
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if !strings.HasPrefix(insertQuery, "INSERT INTO `security`.`venator_findings`") {
		t.Errorf("insert query = %q", insertQuery)
	}
	if prepared.sendCalls != 1 || prepared.closeCalls != 1 {
		t.Errorf("batch send/close calls = %d/%d, want 1/1", prepared.sendCalls, prepared.closeCalls)
	}
	if len(prepared.rows) != 1 || len(prepared.rows[0]) != len(findingColumns) {
		t.Fatalf("inserted shape = %d rows x %d values", len(prepared.rows), len(prepared.rows[0]))
	}
	row := prepared.rows[0]
	if got := row[0].(time.Time); got.Location() != time.UTC || !got.Equal(detectedAt) {
		t.Errorf("detected_at = %v, want UTC %v", got, detectedAt)
	}
	if row[2] != "run-1" || row[3] != finding.ID || row[4] != "rule-1" || row[10] != "alice" || row[16] != "192.0.2.1" {
		t.Errorf("unexpected scalar row = %#v", row)
	}
	if tags := row[22].([]string); len(tags) != 2 || tags[0] != "auth" {
		t.Errorf("tags = %v", tags)
	}
	var decoded model.Finding
	if err := json.Unmarshal([]byte(row[24].(string)), &decoded); err != nil {
		t.Fatalf("payload is not canonical finding JSON: %v", err)
	}
	if decoded.ID != finding.ID || decoded.Review == nil || decoded.Review.Verdict != "suspicious" {
		t.Errorf("decoded payload = %#v", decoded)
	}
}

func TestSinkPublishValidatesEveryFindingBeforeOpeningBatch(t *testing.T) {
	prepareCalls := 0
	conn := &fakeConnection{prepareBatchFn: func(context.Context, string) (writeBatch, error) {
		prepareCalls++
		return &fakeBatch{}, nil
	}}
	sink, err := NewSink(clientForTest(t, baseConfig(), conn))
	if err != nil {
		t.Fatal(err)
	}
	finding := model.Finding{
		ID:         "not-a-sha256",
		RunID:      "run-1",
		DetectedAt: time.Now(),
		Rule:       model.RuleMetadata{ID: "rule-1"},
	}
	if err := sink.Publish(context.Background(), model.PublishBatch{RunID: "run-1", Findings: []model.Finding{finding}}); err == nil || !strings.Contains(err.Error(), "64 hexadecimal") {
		t.Fatalf("Publish() error = %v, want finding ID validation", err)
	}
	if prepareCalls != 0 {
		t.Fatalf("PrepareBatch calls = %d, want 0", prepareCalls)
	}
}

func TestSinkPublishSurfacesAppendSendAndCloseErrors(t *testing.T) {
	tests := []struct {
		name      string
		batch     *fakeBatch
		wantParts []string
	}{
		{name: "append", batch: &fakeBatch{appendErr: errors.New("append failed"), closeErr: errors.New("close failed")}, wantParts: []string{"append failed", "close failed"}},
		{name: "send", batch: &fakeBatch{sendErr: errors.New("send failed"), closeErr: errors.New("close failed")}, wantParts: []string{"send failed", "close failed"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conn := &fakeConnection{prepareBatchFn: func(context.Context, string) (writeBatch, error) {
				return test.batch, nil
			}}
			sink, err := NewSink(clientForTest(t, baseConfig(), conn))
			if err != nil {
				t.Fatal(err)
			}
			finding := model.Finding{
				ID:         strings.Repeat("0", 64),
				RunID:      "run-1",
				DetectedAt: time.Now(),
				Rule:       model.RuleMetadata{ID: "rule-1"},
			}
			err = sink.Publish(context.Background(), model.PublishBatch{RunID: "run-1", Findings: []model.Finding{finding}})
			for _, want := range test.wantParts {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("Publish() error = %v, want containing %q", err, want)
				}
			}
		})
	}
}

func TestSinkPublishEmptyBatchDoesNotOpenDriverBatch(t *testing.T) {
	conn := &fakeConnection{}
	sink, err := NewSink(clientForTest(t, baseConfig(), conn))
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Publish(context.Background(), model.PublishBatch{}); err != nil {
		t.Fatalf("Publish(empty) error = %v", err)
	}
}

func TestSchemaMatchesSinkColumns(t *testing.T) {
	schema, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range findingColumns {
		if !strings.Contains(string(schema), "    "+column+" ") {
			t.Errorf("schema.sql does not define sink column %q", column)
		}
	}
}
