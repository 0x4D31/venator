package connector

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0x4D31/venator/internal/config"
	"github.com/0x4D31/venator/internal/model"
)

type concurrentQueryRunner struct{ closes atomic.Int32 }

func (*concurrentQueryRunner) Query(context.Context, *config.RuleConfig) ([]model.Record, error) {
	return nil, nil
}
func (r *concurrentQueryRunner) Close() error { r.closes.Add(1); return nil }

type concurrentPublisher struct{ closes atomic.Int32 }

func (*concurrentPublisher) Publish(context.Context, model.PublishBatch) error { return nil }
func (p *concurrentPublisher) Close() error                                    { p.closes.Add(1); return nil }

type blockingConnector struct {
	closeStarted chan struct{}
	releaseClose chan struct{}
	closeErr     error
	closes       atomic.Int32
}

func TestNDJSONProfileIsLazyAndReadsSelectedSnapshot(t *testing.T) {
	dir := t.TempDir()
	events := filepath.Join(dir, "events.ndjson")
	if err := os.WriteFile(events, []byte("{\"kind\":\"failed_login\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	globalPath := filepath.Join(dir, "global.yaml")
	globalYAML := `runtime:
  maxRecords: 10
  maxBytes: 1048576
  timeout: 1m
ndjson:
  instances:
    events:
      path: events.ndjson
    unused:
      path: ${VENATOR_TEST_UNUSED_NDJSON}
`
	if err := os.WriteFile(globalPath, []byte(globalYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	global, err := config.ParseGlobalConfig(globalPath)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(context.Background(), global, strings.NewReader(""), io.Discard)
	t.Cleanup(func() { _ = registry.Close() })
	rule := &config.RuleConfig{Source: "ndjson.events", Publishers: []string{"stdout.default"}}
	if err := registry.ValidateRule(rule); err != nil {
		t.Fatal(err)
	}
	runner, err := registry.GetQueryRunner(rule.Source)
	if err != nil {
		t.Fatal(err)
	}
	records, err := runner.Query(context.Background(), rule)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0]["kind"] != "failed_login" {
		t.Fatalf("records = %#v", records)
	}

	unused := &config.RuleConfig{Source: "ndjson.unused", Publishers: []string{"stdout.default"}}
	if err := registry.ValidateRule(unused); err == nil || !strings.Contains(err.Error(), "VENATOR_TEST_UNUSED_NDJSON") {
		t.Fatalf("selected unset profile error = %v", err)
	}
}

func (*blockingConnector) Query(context.Context, *config.RuleConfig) ([]model.Record, error) {
	return nil, nil
}
func (*blockingConnector) Publish(context.Context, model.PublishBatch) error { return nil }
func (c *blockingConnector) Close() error {
	c.closes.Add(1)
	close(c.closeStarted)
	<-c.releaseClose
	return c.closeErr
}

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
		Source:     "opensearch.logs",
		Publishers: []string{"opensearch.logs"},
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

func TestRegistryPropagatesOpenSearchSQLFetchSize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			FetchSize *int `json:"fetch_size"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.FetchSize == nil || *request.FetchSize != 3 {
			t.Fatalf("fetch_size = %v, want 3", request.FetchSize)
		}
		_, _ = io.WriteString(w, `{"schema":[],"datarows":[],"total":0,"size":0}`)
	}))
	t.Cleanup(server.Close)
	global := &config.GlobalConfig{
		Runtime: config.RuntimeConfig{MaxRecords: 10, MaxBytes: 1 << 20, Timeout: config.Duration(time.Minute)},
		OpenSearch: config.OpenSearchConnectors{Instances: map[string]config.OpenSearchConfig{
			"logs": {URL: server.URL, SQLFetchSize: 3},
		}},
	}
	registry := NewRegistry(context.Background(), global, strings.NewReader(""), io.Discard)
	t.Cleanup(func() { _ = registry.Close() })
	runner, err := registry.GetQueryRunner("opensearch.logs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Query(context.Background(), &config.RuleConfig{Language: "SQL", Query: "SELECT value"}); err != nil {
		t.Fatal(err)
	}
}

func TestWebhookConnectorResolvesEnvironmentWithoutMutatingGlobalConfig(t *testing.T) {
	secret := "whsec_" + base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer local-token" {
			t.Errorf("Authorization = %q", got)
		}
		if r.Header.Get("Webhook-Signature") == "" || r.Header.Get("Webhook-ID") == "" {
			t.Error("signed webhook headers are missing")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	t.Setenv("VENATOR_WEBHOOK_URL", server.URL)
	t.Setenv("VENATOR_WEBHOOK_TOKEN", "local-token")
	t.Setenv("VENATOR_WEBHOOK_SECRET", secret)

	global := &config.GlobalConfig{
		Runtime: config.RuntimeConfig{MaxRecords: 10, MaxBytes: 1 << 20, Timeout: config.Duration(time.Minute)},
		Webhook: config.WebhookConnectors{Instances: map[string]config.WebhookConfig{
			"agent": {
				URL: "${VENATOR_WEBHOOK_URL}", Headers: map[string]string{"Authorization": "Bearer ${VENATOR_WEBHOOK_TOKEN}"},
				SigningSecret: "${VENATOR_WEBHOOK_SECRET}", Timeout: config.Duration(time.Second),
				MaxAttempts: 1, MaxFindings: 10, MaxPayloadBytes: 1 << 20,
			},
		}},
	}
	registry := NewRegistry(context.Background(), global, strings.NewReader(""), io.Discard)
	t.Cleanup(func() { _ = registry.Close() })
	rule := &config.RuleConfig{Source: "stdin.default", Publishers: []string{"webhook.agent"}}
	if err := registry.PreflightRule(rule); err != nil {
		t.Fatal(err)
	}
	publisher, err := registry.GetPublisher("webhook.agent")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	if err := publisher.Publish(context.Background(), model.PublishBatch{
		RunID: "run-1",
		Findings: []model.Finding{{
			SchemaVersion: model.FindingSchemaVersion, ID: "finding-1", RunID: "run-1", DetectedAt: now,
			Source: "stdin.default", OutputFormat: "raw", Rule: model.RuleMetadata{ID: "rule-1"}, Payload: model.Record{"event": "test"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
	original := global.Webhook.Instances["agent"]
	if original.URL != "${VENATOR_WEBHOOK_URL}" || original.Headers["Authorization"] != "Bearer ${VENATOR_WEBHOOK_TOKEN}" || original.SigningSecret != "${VENATOR_WEBHOOK_SECRET}" {
		t.Fatalf("global webhook config was mutated: %#v", original)
	}
}

func TestPreflightDefersBestEffortConnectorFailuresToRun(t *testing.T) {
	registry := NewRegistry(context.Background(), testGlobalConfig(), strings.NewReader(""), io.Discard)
	t.Cleanup(func() { _ = registry.Close() })
	rule := &config.RuleConfig{
		Source:               "stdin.default",
		Publishers:           []string{"stdout.default"},
		BestEffortPublishers: []string{"missing.optional"},
	}
	if err := registry.PreflightRule(rule); err != nil {
		t.Fatalf("PreflightRule() error = %v", err)
	}
	if err := registry.ValidateRule(rule); err == nil || !strings.Contains(err.Error(), `publisher "missing.optional" is not configured`) {
		t.Fatalf("ValidateRule() error = %v", err)
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
	rule := &config.RuleConfig{Source: "stdin.default", Publishers: []string{"bigquery.alerts"}}
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
	rule := &config.RuleConfig{Source: "stdin.default", Publishers: []string{"clickhouse.alerts"}}
	if err := registry.PreflightRule(rule); err == nil || !strings.Contains(err.Error(), "host:port") {
		t.Fatalf("PreflightRule() error = %v", err)
	}
}

func TestClickHousePreflightValidatesExpandedStringReferences(t *testing.T) {
	tests := []struct {
		name        string
		protocol    string
		compression string
		table       string
		wantError   string
	}{
		{name: "valid", protocol: "native", compression: "lz4", table: "findings"},
		{name: "invalid protocol", protocol: "tcp", compression: "lz4", table: "findings", wantError: "unsupported protocol"},
		{name: "invalid compression", protocol: "native", compression: "gzip", table: "findings", wantError: "unsupported compression"},
		{name: "unsafe table", protocol: "native", compression: "lz4", table: "findings;drop", wantError: "unsupported characters"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("VENATOR_TEST_CLICKHOUSE_PROTOCOL", test.protocol)
			t.Setenv("VENATOR_TEST_CLICKHOUSE_COMPRESSION", test.compression)
			t.Setenv("VENATOR_TEST_CLICKHOUSE_DATABASE", "venator")
			t.Setenv("VENATOR_TEST_CLICKHOUSE_TABLE", test.table)
			global := &config.GlobalConfig{
				Runtime: config.RuntimeConfig{MaxRecords: 10, MaxBytes: 1 << 20, Timeout: config.Duration(time.Minute)},
				ClickHouse: config.ClickHouseConnectors{Instances: map[string]config.ClickHouseConfig{
					"dynamic": {
						Addresses:   []string{"127.0.0.1:9000"},
						Protocol:    "${VENATOR_TEST_CLICKHOUSE_PROTOCOL}",
						Compression: "${VENATOR_TEST_CLICKHOUSE_COMPRESSION}",
						Sink: &config.ClickHouseSinkConfig{
							Table: "${VENATOR_TEST_CLICKHOUSE_DATABASE}.${VENATOR_TEST_CLICKHOUSE_TABLE}",
						},
					},
				}},
			}
			registry := NewRegistry(context.Background(), global, strings.NewReader(""), io.Discard)
			t.Cleanup(func() { _ = registry.Close() })
			rule := &config.RuleConfig{Source: "stdin.default", Publishers: []string{"clickhouse.dynamic"}}
			err := registry.PreflightRule(rule)
			if test.wantError == "" && err != nil {
				t.Fatal(err)
			}
			if test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

func TestClickHouseValidationIsConcurrentAndDoesNotMutateGlobalConfig(t *testing.T) {
	t.Setenv("CLICKHOUSE_ADDRESS", "localhost:9000")
	global := &config.GlobalConfig{
		Runtime: config.RuntimeConfig{MaxRecords: 10, MaxBytes: 1 << 20, Timeout: config.Duration(time.Minute)},
		ClickHouse: config.ClickHouseConnectors{Instances: map[string]config.ClickHouseConfig{
			"shared": {
				Addresses: []string{"${CLICKHOUSE_ADDRESS}"},
				Query:     &config.ClickHouseQueryConfig{},
				Sink:      &config.ClickHouseSinkConfig{Table: "venator_findings"},
			},
		}},
	}
	registry := NewRegistry(context.Background(), global, strings.NewReader(""), io.Discard)
	t.Cleanup(func() { _ = registry.Close() })
	queryValidator := registry.queryValidators["clickhouse.shared"]
	publisherValidator := registry.publisherValidators["clickhouse.shared"]
	if queryValidator == nil || publisherValidator == nil {
		t.Fatal("ClickHouse validators were not registered")
	}

	const goroutines = 32
	errs := make(chan error, goroutines)
	var group sync.WaitGroup
	group.Add(goroutines)
	for i := range goroutines {
		validator := queryValidator
		if i%2 != 0 {
			validator = publisherValidator
		}
		go func() {
			defer group.Done()
			errs <- validator(context.Background())
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	original := global.ClickHouse.Instances["shared"]
	if original.Addresses[0] != "${CLICKHOUSE_ADDRESS}" || original.Query.Timeout != 0 || original.Query.MaxRows != 0 {
		t.Fatalf("global ClickHouse config was mutated: %#v", original)
	}
}

func TestRegistrySerializesQueryRunnerInitializationPerKey(t *testing.T) {
	registry := NewRegistry(context.Background(), testGlobalConfig(), strings.NewReader(""), io.Discard)
	want := &concurrentQueryRunner{}
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	registry.queryFactories["test.source"] = func(context.Context) (QueryRunner, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return want, nil
	}

	const goroutines = 32
	results := make(chan QueryRunner, goroutines)
	errs := make(chan error, goroutines)
	var group sync.WaitGroup
	group.Add(goroutines)
	for range goroutines {
		go func() {
			defer group.Done()
			got, err := registry.GetQueryRunner("test.source")
			results <- got
			errs <- err
		}()
	}
	<-started
	close(release)
	group.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for got := range results {
		if got != want {
			t.Fatalf("runner = %p, want %p", got, want)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("factory calls = %d, want 1", got)
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	if got := want.closes.Load(); got != 1 {
		t.Fatalf("close calls = %d, want 1", got)
	}
}

func TestRegistrySerializesPublisherInitializationPerKey(t *testing.T) {
	registry := NewRegistry(context.Background(), testGlobalConfig(), strings.NewReader(""), io.Discard)
	want := &concurrentPublisher{}
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	registry.publisherFactories["test.sink"] = func(context.Context) (Publisher, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return want, nil
	}

	const goroutines = 32
	results := make(chan Publisher, goroutines)
	errs := make(chan error, goroutines)
	var group sync.WaitGroup
	group.Add(goroutines)
	for range goroutines {
		go func() {
			defer group.Done()
			got, err := registry.GetPublisher("test.sink")
			results <- got
			errs <- err
		}()
	}
	<-started
	close(release)
	group.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for got := range results {
		if got != want {
			t.Fatalf("publisher = %p, want %p", got, want)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("factory calls = %d, want 1", got)
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	if got := want.closes.Load(); got != 1 {
		t.Fatalf("close calls = %d, want 1", got)
	}
}

func TestRegistryCloseWaitsForFailedInitializationCleanup(t *testing.T) {
	tests := []struct {
		name  string
		start func(*Registry, *blockingConnector, chan struct{}, chan struct{}) <-chan error
	}{
		{
			name: "source",
			start: func(registry *Registry, connector *blockingConnector, started, release chan struct{}) <-chan error {
				registry.queryFactories["test.late"] = func(context.Context) (QueryRunner, error) {
					close(started)
					<-release
					return connector, nil
				}
				result := make(chan error, 1)
				go func() {
					_, err := registry.GetQueryRunner("test.late")
					result <- err
				}()
				return result
			},
		},
		{
			name: "publisher",
			start: func(registry *Registry, connector *blockingConnector, started, release chan struct{}) <-chan error {
				registry.publisherFactories["test.late"] = func(context.Context) (Publisher, error) {
					close(started)
					<-release
					return connector, nil
				}
				result := make(chan error, 1)
				go func() {
					_, err := registry.GetPublisher("test.late")
					result <- err
				}()
				return result
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry(context.Background(), testGlobalConfig(), strings.NewReader(""), io.Discard)
			cleanupErr := errors.New("cleanup failed")
			connector := &blockingConnector{
				closeStarted: make(chan struct{}),
				releaseClose: make(chan struct{}),
				closeErr:     cleanupErr,
			}
			factoryStarted := make(chan struct{})
			releaseFactory := make(chan struct{})
			getResult := test.start(registry, connector, factoryStarted, releaseFactory)
			<-factoryStarted

			closeResult := make(chan error, 1)
			go func() { closeResult <- registry.Close() }()
			deadline := time.Now().Add(time.Second)
			for {
				registry.mu.Lock()
				closed := registry.closed
				registry.mu.Unlock()
				if closed {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("registry did not start closing")
				}
				runtime.Gosched()
			}

			close(releaseFactory)
			<-connector.closeStarted
			select {
			case err := <-closeResult:
				t.Fatalf("Close returned before initialization cleanup completed: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			close(connector.releaseClose)

			if err := <-getResult; err == nil || !strings.Contains(err.Error(), "connector registry is closed") ||
				!errors.Is(err, cleanupErr) {
				t.Fatalf("initialization error = %v", err)
			}
			if err := <-closeResult; !errors.Is(err, cleanupErr) {
				t.Fatalf("Close error = %v, want cleanup error", err)
			}
			if got := connector.closes.Load(); got != 1 {
				t.Fatalf("close calls = %d, want 1", got)
			}
		})
	}
}

func testGlobalConfig() *config.GlobalConfig {
	return &config.GlobalConfig{Runtime: config.RuntimeConfig{
		MaxRecords: 10,
		MaxBytes:   1 << 20,
		Timeout:    config.Duration(time.Minute),
	}}
}
