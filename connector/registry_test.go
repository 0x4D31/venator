package connector

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/0x4D31/venator/internal/config"
)

func TestOpenSearchPreflightPreservesLiteralDollarPasswordAcrossRoles(t *testing.T) {
	t.Setenv("OPENSEARCH_PASSWORD", "$2b$12$literal-value")
	global := &config.GlobalConfig{
		Runtime: config.RuntimeConfig{MaxRecords: 10, MaxBytes: 1 << 20, Timeout: config.Duration(time.Minute)},
		OpenSearch: config.OpenSearchConnectors{Instances: map[string]config.OpenSearchConfig{
			"logs": {
				URL:      "http://127.0.0.1:9200",
				Username: "admin",
				Password: "${OPENSEARCH_PASSWORD}",
			},
		}},
	}
	registry := NewRegistry(context.Background(), global, strings.NewReader(""), io.Discard)
	defer registry.Close()
	rule := &config.RuleConfig{
		QueryEngine: "opensearch.logs",
		Publishers:  []string{"opensearch.logs"},
	}
	if err := registry.PreflightRule(rule); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.GetQueryRunner("opensearch.logs"); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.GetPublisher("opensearch.logs"); err != nil {
		t.Fatal(err)
	}
}

func TestBigQuerySinkPreflightRejectsEnvironmentExpandedEmptyRole(t *testing.T) {
	t.Setenv("BIGQUERY_DATASET", "")
	t.Setenv("BIGQUERY_TABLE", "")
	global := &config.GlobalConfig{
		Runtime: config.RuntimeConfig{MaxRecords: 10, MaxBytes: 1 << 20, Timeout: config.Duration(time.Minute)},
		BigQuery: config.BigQueryConnectors{Instances: map[string]config.BigQueryConfig{
			"alerts": {
				ProjectID: "test-project", DatasetID: "${BIGQUERY_DATASET}", TableID: "${BIGQUERY_TABLE}",
			},
		}},
	}
	registry := NewRegistry(context.Background(), global, strings.NewReader(""), io.Discard)
	defer registry.Close()
	rule := &config.RuleConfig{QueryEngine: "stdin.default", Publishers: []string{"bigquery.alerts"}}
	if err := registry.PreflightRule(rule); err == nil || !strings.Contains(err.Error(), "non-empty datasetID and tableID") {
		t.Fatalf("PreflightRule() error = %v", err)
	}
}

func TestClickHouseSinkPreflightRejectsAddressWithoutPort(t *testing.T) {
	global := &config.GlobalConfig{
		Runtime: config.RuntimeConfig{MaxRecords: 10, MaxBytes: 1 << 20, Timeout: config.Duration(time.Minute)},
		ClickHouse: config.ClickHouseConnectors{Instances: map[string]config.ClickHouseConfig{
			"alerts": {
				Addresses: []string{"localhost"},
				Sink:      &config.ClickHouseSinkConfig{Table: "venator_findings"},
			},
		}},
	}
	registry := NewRegistry(context.Background(), global, strings.NewReader(""), io.Discard)
	defer registry.Close()
	rule := &config.RuleConfig{QueryEngine: "stdin.default", Publishers: []string{"clickhouse.alerts"}}
	if err := registry.PreflightRule(rule); err == nil || !strings.Contains(err.Error(), "host:port") {
		t.Fatalf("PreflightRule() error = %v", err)
	}
}
