package opensearch_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/0x4D31/venator/connector/opensearch"
	"github.com/0x4D31/venator/internal/config"
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	installOpenSearchSampleData(t, ctx, dashboardsEndpoint, username, password)

	client, err := opensearch.New(ctx, opensearch.Config{
		URL:                endpoint,
		Username:           username,
		Password:           password,
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("Close() error: %v", err)
		}
	})

	records, err := client.Query(ctx, &config.RuleConfig{
		Query: fmt.Sprintf("SELECT timestamp, ip, host, request FROM %s LIMIT 2", index),
	})
	if err != nil {
		t.Fatalf("Query() error: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("Query() returned %d records, want 2", len(records))
	}

	if err := client.Publish(ctx, testBatch()); err != nil {
		t.Fatalf("Publish() error: %v", err)
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
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{ // #nosec G402 -- local integration environment only.
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true,
	}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("install sample data: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		t.Fatalf("install sample data returned %s", resp.Status)
	}
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
