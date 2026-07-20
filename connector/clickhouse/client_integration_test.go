package clickhouse_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/0x4D31/venator/connector/clickhouse"
	"github.com/0x4D31/venator/internal/config"
	"github.com/0x4D31/venator/internal/model"
)

func TestLiveNativeAndHTTP(t *testing.T) {
	if os.Getenv("VENATOR_CLICKHOUSE_INTEGRATION") != "1" {
		t.Skip("set VENATOR_CLICKHOUSE_INTEGRATION=1 to run against a live ClickHouse server")
	}
	password := os.Getenv("VENATOR_CLICKHOUSE_PASSWORD")
	if password == "" {
		t.Fatal("VENATOR_CLICKHOUSE_PASSWORD is required for the live ClickHouse test")
	}

	tests := []struct {
		protocol    string
		compression string
		address     string
		findingID   string
	}{
		{
			protocol:    "native",
			compression: "lz4",
			address:     environmentOr("VENATOR_CLICKHOUSE_NATIVE_ADDRESS", "127.0.0.1:9000"),
			findingID:   strings.Repeat("a", 64),
		},
		{
			protocol:    "http",
			compression: "none",
			address:     environmentOr("VENATOR_CLICKHOUSE_HTTP_ADDRESS", "127.0.0.1:8123"),
			findingID:   strings.Repeat("b", 64),
		},
	}
	for _, test := range tests {
		t.Run(test.protocol, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			client, err := clickhouse.Open(ctx, config.ClickHouseConfig{
				Addresses:   []string{test.address},
				Protocol:    test.protocol,
				Database:    "venator",
				Username:    environmentOr("VENATOR_CLICKHOUSE_USERNAME", "venator"),
				Password:    password,
				Compression: test.compression,
				Query: &config.ClickHouseQueryConfig{
					Timeout:  config.Duration(10 * time.Second),
					MaxRows:  100,
					MaxBytes: 1 << 20,
				},
				Sink: &config.ClickHouseSinkConfig{Table: "venator.findings"},
			})
			if err != nil {
				t.Fatalf("open %s client: %v", test.protocol, err)
			}
			t.Cleanup(func() {
				if err := client.Close(); err != nil {
					t.Errorf("close %s client: %v", test.protocol, err)
				}
			})

			source, err := client.Source()
			if err != nil {
				t.Fatal(err)
			}
			sink, err := client.Sink()
			if err != nil {
				t.Fatal(err)
			}

			assertTypedQuery(t, ctx, source)
			assertFindingRoundTrip(t, ctx, source, sink, test.protocol, test.findingID)
		})
	}
}

type querySource interface {
	Query(context.Context, *config.RuleConfig) ([]model.Record, error)
}

type findingSink interface {
	Publish(context.Context, model.PublishBatch) error
}

func assertTypedQuery(t *testing.T, ctx context.Context, source querySource) {
	t.Helper()
	records, err := source.Query(ctx, &config.RuleConfig{Query: `
		SELECT
			toInt64(42) AS integer_value,
			CAST(NULL AS Nullable(String)) AS optional_value,
			toDateTime64('2026-07-19 12:34:56.123456', 6, 'UTC') AS event_time
	`})
	if err != nil {
		t.Fatalf("typed query: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("typed query returned %d records, want 1", len(records))
	}
	if got, ok := records[0]["integer_value"].(int64); !ok || got != 42 {
		t.Fatalf("integer_value = %#v (%T), want int64(42)", records[0]["integer_value"], records[0]["integer_value"])
	}
	if records[0]["optional_value"] != nil {
		t.Fatalf("optional_value = %#v, want nil", records[0]["optional_value"])
	}
	wantTime := time.Date(2026, time.July, 19, 12, 34, 56, 123456000, time.UTC)
	if got, ok := records[0]["event_time"].(time.Time); !ok || !got.Equal(wantTime) {
		t.Fatalf("event_time = %#v (%T), want %s", records[0]["event_time"], records[0]["event_time"], wantTime)
	}
}

func assertFindingRoundTrip(t *testing.T, ctx context.Context, source querySource, sink findingSink, protocol, findingID string) {
	t.Helper()
	detectedAt := time.Date(2026, time.July, 19, 13, 0, 0, 123456000, time.UTC)
	eventAt := time.Date(2026, time.July, 19, 12, 59, 59, 654321000, time.UTC)
	rule := model.RuleMetadata{
		ID:         "6722b4ed-f891-4906-a4b2-f57762dfc72b",
		Name:       "ClickHouse " + protocol + " integration",
		Status:     "test",
		Confidence: "high",
		Tags:       []string{"integration"},
		TTPIDs:     []string{"T1078"},
	}
	finding := model.Finding{
		SchemaVersion: model.FindingSchemaVersion,
		ID:            findingID,
		RunID:         "clickhouse-integration-" + protocol,
		DetectedAt:    detectedAt,
		EventAt:       &eventAt,
		Source:        "clickhouse.integration",
		OutputFormat:  "signal",
		Rule:          rule,
		Attributes: model.FindingAttributes{
			ActorUserName: "alice",
			Message:       "integration finding",
			EventID:       "event-" + protocol,
		},
		Payload: model.Record{"protocol": protocol, "success": false, "count": int64(42)},
	}
	if err := sink.Publish(ctx, model.PublishBatch{
		RunID:      finding.RunID,
		DetectedAt: detectedAt,
		Source:     finding.Source,
		Rule:       rule,
		Findings:   []model.Finding{finding},
	}); err != nil {
		t.Fatalf("publish finding: %v", err)
	}

	records, err := source.Query(ctx, &config.RuleConfig{Query: `
		SELECT finding_id, event_at, payload
		FROM venator.findings
		WHERE finding_id = '` + findingID + `'
		ORDER BY detected_at DESC
		LIMIT 1
	`})
	if err != nil {
		t.Fatalf("query published finding: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("published finding query returned %d records, want 1", len(records))
	}
	if got := records[0]["finding_id"]; got != findingID {
		t.Fatalf("finding_id = %#v (%T), want %q", got, got, findingID)
	}
	if got, ok := records[0]["event_at"].(time.Time); !ok || !got.Equal(eventAt) {
		t.Fatalf("event_at = %#v (%T), want %s", records[0]["event_at"], records[0]["event_at"], eventAt)
	}
	payload, ok := records[0]["payload"].(string)
	if !ok {
		t.Fatalf("payload = %#v (%T), want string", records[0]["payload"], records[0]["payload"])
	}
	var roundTrip model.Finding
	if err := json.Unmarshal([]byte(payload), &roundTrip); err != nil {
		t.Fatalf("decode stored canonical finding: %v", err)
	}
	if roundTrip.ID != finding.ID || roundTrip.RunID != finding.RunID || roundTrip.Attributes.Message != finding.Attributes.Message {
		t.Fatalf("stored finding = %#v", roundTrip)
	}
}

func environmentOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
