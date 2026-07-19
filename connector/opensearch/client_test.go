package opensearch_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/0x4D31/venator/connector/opensearch"
	"github.com/0x4D31/venator/internal/config"
	"github.com/0x4D31/venator/internal/model"
)

func TestQueryPreservesNativeTypes(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_plugins/_sql" {
			t.Fatalf("unexpected request path %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method %q", r.Method)
		}
		if username, password, ok := r.BasicAuth(); !ok || username != "user" || password != "password" {
			t.Fatalf("unexpected basic auth: username=%q password=%q ok=%v", username, password, ok)
		}
		var request map[string]string
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request["query"] != "SELECT * FROM logs" {
			t.Fatalf("unexpected query %q", request["query"])
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"schema":[
				{"name":"count"},
				{"name":"active"},
				{"name":"context"},
				{"name":"original","alias":"renamed"},
				{"name":"optional"}
			],
			"datarows":[[2,true,{"host":"local"},"value",null]],
			"total":1,
			"size":1,
			"status":200
		}`)
	}))

	got, err := client.Query(context.Background(), &config.RuleConfig{Query: "SELECT * FROM logs"})
	if err != nil {
		t.Fatalf("Query() error: %v", err)
	}
	want := []model.Record{{
		"count":    json.Number("2"),
		"active":   true,
		"context":  map[string]any{"host": "local"},
		"renamed":  "value",
		"optional": nil,
	}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("Query() mismatch (-want +got):\n%s", diff)
	}
}

func TestQueryPreservesLargeIntegerPrecision(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"schema":[{"name":"event_id"}],"datarows":[[9007199254740993]]}`)
	}))
	records, err := client.Query(context.Background(), &config.RuleConfig{Query: "SELECT event_id"})
	if err != nil {
		t.Fatal(err)
	}
	value, ok := records[0]["event_id"].(json.Number)
	if !ok || value.String() != "9007199254740993" {
		t.Fatalf("event_id = %#v", records[0]["event_id"])
	}
}

func TestQueryEnforcesCumulativeResponseByteLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"schema":[{"name":"value"}],"datarows":[["too large"]]}`)
	}))
	defer server.Close()
	client, err := opensearch.New(context.Background(), opensearch.Config{URL: server.URL, MaxBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.Query(context.Background(), &config.RuleConfig{Query: "SELECT value"})
	if err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("error = %v", err)
	}
}

func TestQuerySelectsPPLPath(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_plugins/_ppl" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"schema":[],"datarows":[]}`)
	}))
	if _, err := client.Query(context.Background(), &config.RuleConfig{Language: "PPL", Query: "source=logs"}); err != nil {
		t.Fatal(err)
	}
}

func TestQueryFollowsCursorPages(t *testing.T) {
	var requests atomic.Int32
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := requests.Add(1)
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if call == 1 {
			if body["query"] == "" {
				t.Fatalf("first body = %#v", body)
			}
			_, _ = io.WriteString(w, `{"schema":[{"name":"value"}],"datarows":[[1]],"cursor":"next"}`)
			return
		}
		if body["cursor"] != "next" {
			t.Fatalf("second body = %#v", body)
		}
		_, _ = io.WriteString(w, `{"datarows":[[2]]}`)
	}))
	got, err := client.Query(context.Background(), &config.RuleConfig{Language: "SQL", Query: "SELECT value"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1]["value"] != json.Number("2") {
		t.Fatalf("records = %#v", got)
	}
}

func TestQueryRejectsMalformedRows(t *testing.T) {
	tests := []struct {
		name     string
		response string
		want     string
	}{
		{
			name:     "short row",
			response: `{"schema":[{"name":"one"},{"name":"two"}],"datarows":[[1]]}`,
			want:     "row 0 has 1 values for 2 columns",
		},
		{
			name:     "long row",
			response: `{"schema":[{"name":"one"}],"datarows":[[1,2]]}`,
			want:     "row 0 has 2 values for 1 columns",
		},
		{
			name:     "missing column name",
			response: `{"schema":[{}],"datarows":[[1]]}`,
			want:     "column 0 has no name",
		},
		{
			name:     "duplicate column name",
			response: `{"schema":[{"name":"same"},{"alias":"same"}],"datarows":[[1,2]]}`,
			want:     `duplicate column "same"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tt.response)
			}))
			_, err := client.Query(context.Background(), &config.RuleConfig{Query: "query"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Query() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestQueryReportsHTTPError(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"reason":"Invalid SQL query","details":"unexpected FROM"},"status":400}`)
	}))

	_, err := client.Query(context.Background(), &config.RuleConfig{Query: "SELECT FROM"})
	if err == nil {
		t.Fatal("Query() error = nil, want an error")
	}
	for _, want := range []string{"400 Bad Request", "Invalid SQL query", "unexpected FROM"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Query() error = %q, want substring %q", err, want)
		}
	}
}

func TestQueryRejectsErrorStatusInResponseBody(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"schema":[],"datarows":[],"status":503}`)
	}))

	_, err := client.Query(context.Background(), &config.RuleConfig{Query: "query"})
	if err == nil || !strings.Contains(err.Error(), "reported status 503") {
		t.Fatalf("Query() error = %v, want body status error", err)
	}
}

func TestQueryRequiresRuleConfig(t *testing.T) {
	var requests atomic.Int32
	client := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))

	_, err := client.Query(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "rule config is required") {
		t.Fatalf("Query() error = %v, want missing config error", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("server received %d request(s), want 0", got)
	}
}

func TestQueryHonorsCanceledContext(t *testing.T) {
	var requests atomic.Int32
	client := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.Query(ctx, &config.RuleConfig{Query: "query"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Query() error = %v, want context.Canceled", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("server received %d request(s), want 0", got)
	}
}

func TestAnonymousQueryAndCursorCloseDoNotSendBasicAuth(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if authorization := r.Header.Get("Authorization"); authorization != "" {
			t.Errorf("Authorization = %q", authorization)
		}
		switch r.URL.Path {
		case "/_plugins/_sql":
			_, _ = io.WriteString(w, `{"schema":[{"name":"value"}],"datarows":[[1],[2]],"cursor":"next"}`)
		case "/_plugins/_sql/close":
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Errorf("path = %q", r.URL.Path)
		}
	}))
	defer server.Close()
	client, err := opensearch.New(context.Background(), opensearch.Config{URL: server.URL, MaxRows: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.Query(context.Background(), &config.RuleConfig{Language: "SQL", Query: "SELECT value"})
	if err == nil || !strings.Contains(err.Error(), "row limit") {
		t.Fatalf("error = %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d", got)
	}
}

func TestPublishWritesCanonicalFindingWithStableID(t *testing.T) {
	requestBody := make(chan []byte, 1)
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_bulk" {
			t.Fatalf("unexpected request path %q", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		requestBody <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"errors":false,"items":[{"index":{"status":201}}]}`)
	}))

	batch := testBatch()
	if err := client.Publish(context.Background(), batch); err != nil {
		t.Fatalf("Publish() error: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(<-requestBody)), "\n")
	if len(lines) != 2 {
		t.Fatalf("bulk request has %d lines, want 2: %q", len(lines), lines)
	}
	var action struct {
		Index struct {
			Index string `json:"_index"`
			ID    string `json:"_id"`
		} `json:"index"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &action); err != nil {
		t.Fatalf("decode bulk action: %v", err)
	}
	if action.Index.Index != "signals" || action.Index.ID != batch.Findings[0].ID {
		t.Fatalf("unexpected bulk action: %+v", action.Index)
	}
	var finding model.Finding
	if err := json.Unmarshal([]byte(lines[1]), &finding); err != nil {
		t.Fatalf("decode finding: %v", err)
	}
	if finding.ID != batch.Findings[0].ID || finding.RunID != batch.Findings[0].RunID {
		t.Fatalf("unexpected canonical finding: %+v", finding)
	}
	if diff := cmp.Diff(batch.Findings[0].Payload, finding.Payload); diff != "" {
		t.Fatalf("finding payload mismatch (-want +got):\n%s", diff)
	}
}

func TestPublishRejectsFailures(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		response   string
		want       string
	}{
		{
			name:       "HTTP failure",
			statusCode: http.StatusInternalServerError,
			response:   `{"error":"cluster unavailable"}`,
			want:       "500 Internal Server Error",
		},
		{
			name:       "item failure",
			statusCode: http.StatusOK,
			response:   `{"errors":true,"items":[{"index":{"status":400,"error":{"reason":"bad document"}}}]}`,
			want:       "bad document",
		},
		{
			name:       "missing item",
			statusCode: http.StatusOK,
			response:   `{"errors":false,"items":[]}`,
			want:       "0 items for 1 findings",
		},
		{
			name:       "missing item status",
			statusCode: http.StatusOK,
			response:   `{"errors":false,"items":[{"index":{}}]}`,
			want:       "returned status 0",
		},
		{
			name:       "invalid response",
			statusCode: http.StatusOK,
			response:   `not-json`,
			want:       "decode OpenSearch bulk response",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = io.WriteString(w, tt.response)
			}))
			err := client.Publish(context.Background(), testBatch())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Publish() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestPublishValidatesFindingBeforeRequest(t *testing.T) {
	var requests atomic.Int32
	client := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	batch := testBatch()
	batch.Findings[0].ID = ""

	err := client.Publish(context.Background(), batch)
	if err == nil || !strings.Contains(err.Error(), "has no ID") {
		t.Fatalf("Publish() error = %v, want missing ID error", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("server received %d request(s), want 0", got)
	}
}

func TestPublishHonorsCanceledContext(t *testing.T) {
	var requests atomic.Int32
	client := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := client.Publish(ctx, testBatch())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Publish() error = %v, want context.Canceled", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("server received %d request(s), want 0", got)
	}
}

func TestNewRejectsInvalidURL(t *testing.T) {
	for _, rawURL := range []string{"", "localhost:9200", "ftp://localhost"} {
		t.Run(fmt.Sprintf("url=%q", rawURL), func(t *testing.T) {
			_, err := opensearch.New(context.Background(), opensearch.Config{URL: rawURL})
			if err == nil {
				t.Fatalf("New(%q) error = nil, want an error", rawURL)
			}
		})
	}
}

func newTestClient(t *testing.T, handler http.Handler) *opensearch.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := opensearch.New(context.Background(), opensearch.Config{
		URL:      server.URL,
		Username: "user",
		Password: "password",
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("Close() error: %v", err)
		}
	})
	return client
}

func testBatch() model.PublishBatch {
	detectedAt := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	rule := model.RuleMetadata{ID: "rule-1", Name: "Test rule", Confidence: "high"}
	return model.PublishBatch{
		RunID:      "run-1",
		DetectedAt: detectedAt,
		Source:     "opensearch.logs",
		Rule:       rule,
		Findings: []model.Finding{{
			SchemaVersion: model.FindingSchemaVersion,
			ID:            "finding-1",
			RunID:         "run-1",
			DetectedAt:    detectedAt,
			Source:        "opensearch.logs",
			OutputFormat:  "signal",
			Rule:          rule,
			Payload:       map[string]any{"count": float64(2), "active": true},
		}},
	}
}
