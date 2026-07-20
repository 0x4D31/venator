package opensearch_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/0x4D31/venator/connector/opensearch"
	"github.com/0x4D31/venator/internal/config"
	"github.com/0x4D31/venator/internal/model"
)

// TestOpenSearchIntegration is intentionally opt-in. Start the compose stack
// in this directory, then run:
//
// VENATOR_OPENSEARCH_INTEGRATION=1 go test -run TestOpenSearchIntegration ./connector/opensearch
func TestOpenSearchIntegration(t *testing.T) {
	if testing.Short() || os.Getenv("VENATOR_OPENSEARCH_INTEGRATION") != "1" {
		t.Skip("set VENATOR_OPENSEARCH_INTEGRATION=1 to run the live OpenSearch test")
	}

	endpoint := envOrDefault("VENATOR_OPENSEARCH_URL", "https://localhost:9200")
	dashboardsEndpoint := envOrDefault("VENATOR_OPENSEARCH_DASHBOARDS_URL", "http://localhost:5601")
	username := envOrDefault("VENATOR_OPENSEARCH_USERNAME", "admin")
	password := os.Getenv("VENATOR_OPENSEARCH_PASSWORD")
	if password == "" {
		t.Fatal("VENATOR_OPENSEARCH_PASSWORD is required for the opt-in integration test")
	}
	index := envOrDefault("VENATOR_OPENSEARCH_TEST_INDEX", "opensearch_dashboards_sample_data_logs")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	installOpenSearchSampleData(t, ctx, dashboardsEndpoint, username, password)
	installOpenSearchIndexTemplate(t, ctx, endpoint, username, password)

	aggregateClient, err := opensearch.New(ctx, opensearch.Config{
		URL:                endpoint,
		Username:           username,
		Password:           password,
		InsecureSkipVerify: true,
		MaxRows:            2_000,
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	t.Cleanup(func() {
		if err := aggregateClient.Close(); err != nil {
			t.Errorf("Close() error: %v", err)
		}
	})
	aggregates, err := aggregateClient.Query(ctx, &config.RuleConfig{
		Query: fmt.Sprintf("SELECT COUNT(*) AS event_count FROM %s", index),
	})
	if err != nil {
		t.Fatalf("aggregate Query() error: %v", err)
	}
	if len(aggregates) != 1 {
		t.Fatalf("aggregate Query() returned %d records, want 1", len(aggregates))
	}

	pagedClient, err := opensearch.New(ctx, opensearch.Config{
		URL:                endpoint,
		Username:           username,
		Password:           password,
		InsecureSkipVerify: true,
		MaxRows:            2_000,
		SQLFetchSize:       1_000,
	})
	if err != nil {
		t.Fatalf("New() paged client error: %v", err)
	}
	t.Cleanup(func() {
		if err := pagedClient.Close(); err != nil {
			t.Errorf("Close() paged client error: %v", err)
		}
	})
	records, err := pagedClient.Query(ctx, &config.RuleConfig{
		Query: fmt.Sprintf("SELECT timestamp, ip, host, request FROM %s LIMIT 1001", index),
	})
	if err != nil {
		t.Fatalf("Query() error: %v", err)
	}
	if len(records) != 1_001 {
		t.Fatalf("Query() returned %d records, want 1001", len(records))
	}

	batch := testBatch()
	second := batch.Findings[0]
	second.ID = "finding-2"
	batch.Findings[0].Payload = model.Record{"shape": "scalar"}
	second.Payload = model.Record{"shape": model.Record{"nested": true}}
	batch.Findings = append(batch.Findings, second)
	if err := pagedClient.Publish(ctx, batch); err != nil {
		t.Fatalf("Publish() error: %v", err)
	}
}

func installOpenSearchIndexTemplate(t *testing.T, ctx context.Context, endpoint, username, password string) {
	t.Helper()
	template, err := os.ReadFile("index-template.json")
	if err != nil {
		t.Fatalf("read index template: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint+"/_index_template/venator-findings-v1", bytes.NewReader(template))
	if err != nil {
		t.Fatalf("create index-template request: %v", err)
	}
	req.SetBasicAuth(username, password)
	req.Header.Set("Content-Type", "application/json")
	resp, err := integrationHTTPClient().Do(req)
	if err != nil {
		t.Fatalf("install index template: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		t.Fatalf("install index template returned %s: %s", resp.Status, body)
	}
}

func installOpenSearchSampleData(t *testing.T, ctx context.Context, endpoint, username, password string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/api/sample_data/logs", nil)
	if err != nil {
		t.Fatalf("create sample-data request: %v", err)
	}
	req.SetBasicAuth(username, password)
	req.Header.Set("osd-xsrf", "true")
	req.Header.Set("Content-Type", "application/json")
	resp, err := integrationHTTPClient().Do(req)
	if err != nil {
		t.Fatalf("install sample data: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		t.Fatalf("install sample data returned %s", resp.Status)
	}
}

func integrationHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{ // #nosec G402 -- local integration environment only.
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true,
	}
	return &http.Client{Transport: transport, Timeout: 30 * time.Second}
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
