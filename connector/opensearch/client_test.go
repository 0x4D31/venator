package opensearch_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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
		var request opensearch.QueryRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request.Query != "SELECT * FROM logs" || request.FetchSize == nil || *request.FetchSize != 0 {
			t.Fatalf("unexpected request %#v", request)
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

func TestQueryRunsAggregateWithoutCursorPagination(t *testing.T) {
	const query = "SELECT outcome, COUNT(*) AS count FROM logs GROUP BY outcome"
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request opensearch.QueryRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Query != query || request.FetchSize == nil || *request.FetchSize != 0 {
			t.Fatalf("request = %#v", request)
		}
		_, _ = io.WriteString(w, `{"schema":[{"name":"outcome"},{"name":"count"}],"datarows":[["failure",12]],"total":1,"size":1}`)
	}))

	got, err := client.Query(context.Background(), &config.RuleConfig{Language: "SQL", Query: query})
	if err != nil {
		t.Fatal(err)
	}
	want := []model.Record{{"outcome": "failure", "count": json.Number("12")}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("records mismatch (-want +got):\n%s", diff)
	}
}

func TestQueryPreservesLargeIntegerPrecision(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"schema":[{"name":"event_id"}],"datarows":[[9007199254740993]],"total":1,"size":1}`)
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
		var request opensearch.QueryRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.FetchSize != nil {
			t.Fatalf("PPL request unexpectedly set fetch_size: %#v", request)
		}
		_, _ = io.WriteString(w, `{"schema":[],"datarows":[],"total":0,"size":0}`)
	}))
	if _, err := client.Query(context.Background(), &config.RuleConfig{Language: "PPL", Query: "source=logs"}); err != nil {
		t.Fatal(err)
	}
}

func TestQueryFollowsCursorPages(t *testing.T) {
	var requests atomic.Int32
	client := newTestClientWithConfig(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := requests.Add(1)
		var body opensearch.QueryRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if call == 1 {
			if body.Query == "" || body.FetchSize == nil || *body.FetchSize != 1_000 {
				t.Fatalf("first body = %#v", body)
			}
			_, _ = io.WriteString(w, `{"schema":[{"name":"value","type":"long"}],"datarows":[[1]],"total":2,"size":1,"cursor":"next"}`)
			return
		}
		if body.Cursor != "next" || body.Query != "" || body.FetchSize != nil {
			t.Fatalf("second body = %#v", body)
		}
		_, _ = io.WriteString(w, `{"datarows":[[2]],"size":1}`)
	}), opensearch.Config{SQLFetchSize: 1_000})
	got, err := client.Query(context.Background(), &config.RuleConfig{Language: "SQL", Query: "SELECT value"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1]["value"] != json.Number("2") {
		t.Fatalf("records = %#v", got)
	}
}

func TestQueryRejectsUnexpectedCursorWhenPaginationDisabled(t *testing.T) {
	var requests atomic.Int32
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/_plugins/_sql":
			_, _ = io.WriteString(w, `{"schema":[{"name":"value"}],"datarows":[[1]],"total":1,"size":1,"cursor":"unexpected"}`)
		case "/_plugins/_sql/close":
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Fatalf("path = %q", r.URL.Path)
		}
	}))
	_, err := client.Query(context.Background(), &config.RuleConfig{Language: "SQL", Query: "SELECT value"})
	if err == nil || !strings.Contains(err.Error(), "sqlFetchSize is disabled") {
		t.Fatalf("error = %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want query and cursor close", got)
	}
}

func TestQueryAcceptsSQLLimitWhenTotalCountsMatchingDocuments(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"schema":[{"name":"value"}],"datarows":[[1],[2]],"total":4,"size":2}`)
	}))
	records, err := client.Query(context.Background(), &config.RuleConfig{Language: "SQL", Query: "SELECT value FROM logs LIMIT 2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2", len(records))
	}
}

func TestQueryDoesNotInferPPLCompletenessFromTotal(t *testing.T) {
	var paths []string
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_, _ = io.WriteString(w, `{"schema":[{"name":"value"}],"datarows":[[1]],"total":2,"size":1}`)
	}))
	records, err := client.Query(context.Background(), &config.RuleConfig{Language: "PPL", Query: "source=logs | head 1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if diff := cmp.Diff([]string{"/_plugins/_ppl"}, paths); diff != "" {
		t.Fatalf("request paths mismatch (-want +got):\n%s", diff)
	}
}

func TestQueryRejectsInconsistentSize(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"schema":[{"name":"value"}],"datarows":[[1]],"total":1,"size":2}`)
	}))
	_, err := client.Query(context.Background(), &config.RuleConfig{Query: "SELECT value"})
	if err == nil || !strings.Contains(err.Error(), "size 2 does not match 1 rows") {
		t.Fatalf("error = %v", err)
	}
}

func TestQueryRequiresInitialSize(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"schema":[],"datarows":[],"total":0}`)
	}))
	_, err := client.Query(context.Background(), &config.RuleConfig{Query: "SELECT value"})
	if err == nil || !strings.Contains(err.Error(), "missing size") {
		t.Fatalf("error = %v", err)
	}
}

func TestPPLRejectsCursorWithoutFollowingOrClosingIt(t *testing.T) {
	var paths []string
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_, _ = io.WriteString(w, `{"schema":[{"name":"value"}],"datarows":[[1]],"total":1,"size":1,"cursor":"unsupported"}`)
	}))
	_, err := client.Query(context.Background(), &config.RuleConfig{Language: "PPL", Query: "source=logs"})
	if err == nil || !strings.Contains(err.Error(), "cursor pagination is not documented") {
		t.Fatalf("error = %v", err)
	}
	if diff := cmp.Diff([]string{"/_plugins/_ppl"}, paths); diff != "" {
		t.Fatalf("request paths mismatch (-want +got):\n%s", diff)
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
			response: `{"schema":[{"name":"one"},{"name":"two"}],"datarows":[[1]],"total":1,"size":1}`,
			want:     "row 0 has 1 values for 2 columns",
		},
		{
			name:     "long row",
			response: `{"schema":[{"name":"one"}],"datarows":[[1,2]],"total":1,"size":1}`,
			want:     "row 0 has 2 values for 1 columns",
		},
		{
			name:     "missing column name",
			response: `{"schema":[{}],"datarows":[[1]],"total":1,"size":1}`,
			want:     "column 0 has no name",
		},
		{
			name:     "duplicate column name",
			response: `{"schema":[{"name":"same"},{"alias":"same"}],"datarows":[[1,2]],"total":1,"size":1}`,
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
			_, _ = io.WriteString(w, `{"schema":[{"name":"value"}],"datarows":[[1],[2]],"total":2,"size":2,"cursor":"next"}`)
		case "/_plugins/_sql/close":
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Errorf("path = %q", r.URL.Path)
		}
	}))
	defer server.Close()
	client, err := opensearch.New(context.Background(), opensearch.Config{URL: server.URL, MaxRows: 1, SQLFetchSize: 1})
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
	if action.Index.Index != "venator-findings-v1" || action.Index.ID != batch.Findings[0].ID {
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
	secret := "do-not-print"
	for _, rawURL := range []string{
		"", "localhost:9200", "ftp://localhost",
		"https://user:" + secret + "@localhost:9200",
		"https://localhost:9200?token=" + secret,
		"https://localhost:9200#" + secret,
	} {
		t.Run(fmt.Sprintf("url=%q", rawURL), func(t *testing.T) {
			_, err := opensearch.New(context.Background(), opensearch.Config{URL: rawURL})
			if err == nil {
				t.Fatalf("New(%q) error = nil, want an error", rawURL)
			}
			if strings.Contains(err.Error(), secret) || (rawURL != "" && strings.Contains(err.Error(), rawURL)) {
				t.Fatalf("New() leaked URL in error: %v", err)
			}
		})
	}
}

func TestNewRejectsUnsafeIndex(t *testing.T) {
	secret := "do-not-print"
	for _, index := range []string{"-hidden", "HasUppercase", "contains space", strings.Repeat("a", 256), secret + "!"} {
		t.Run(index, func(t *testing.T) {
			_, err := opensearch.New(context.Background(), opensearch.Config{URL: "http://localhost:9200", Index: index})
			if err == nil || !strings.Contains(err.Error(), "invalid OpenSearch index") {
				t.Fatalf("error = %v", err)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error leaked index value: %v", err)
			}
		})
	}
}

func TestNewRejectsInvalidSQLFetchSize(t *testing.T) {
	for _, cfg := range []opensearch.Config{
		{URL: "http://localhost:9200", SQLFetchSize: -1},
		{URL: "http://localhost:9200", MaxRows: 10, SQLFetchSize: 11},
	} {
		if _, err := opensearch.New(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "SQL fetch size") {
			t.Fatalf("New(%+v) error = %v", cfg, err)
		}
	}
}

func TestPublishUsesConfiguredIndex(t *testing.T) {
	requestBody := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requestBody <- body
		_, _ = io.WriteString(w, `{"errors":false,"items":[{"index":{"status":201}}]}`)
	}))
	t.Cleanup(server.Close)
	client, err := opensearch.New(context.Background(), opensearch.Config{URL: server.URL, Index: "security-findings"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Publish(context.Background(), testBatch()); err != nil {
		t.Fatal(err)
	}
	firstLine := strings.SplitN(string(<-requestBody), "\n", 2)[0]
	if !strings.Contains(firstLine, `"_index":"security-findings"`) {
		t.Fatalf("bulk action = %s", firstLine)
	}
}

func TestTransportErrorDoesNotLeakEndpointPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endpoint := server.URL + "/do-not-print"
	client, err := opensearch.New(context.Background(), opensearch.Config{URL: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	server.Close()
	_, err = client.Query(context.Background(), &config.RuleConfig{Query: "SELECT value"})
	if err == nil || strings.Contains(err.Error(), "do-not-print") {
		t.Fatalf("error = %v", err)
	}
}

func TestIndexTemplateDisablesPayloadMapping(t *testing.T) {
	encoded, err := os.ReadFile("index-template.json")
	if err != nil {
		t.Fatal(err)
	}
	var template struct {
		IndexPatterns []string `json:"index_patterns"`
		Template      struct {
			Mappings struct {
				Dynamic    bool `json:"dynamic"`
				Properties map[string]struct {
					Type        string `json:"type"`
					Enabled     *bool  `json:"enabled"`
					IgnoreAbove int    `json:"ignore_above"`
					Properties  map[string]struct {
						Type        string `json:"type"`
						IgnoreAbove int    `json:"ignore_above"`
					} `json:"properties"`
				} `json:"properties"`
			} `json:"mappings"`
		} `json:"template"`
	}
	if err := json.Unmarshal(encoded, &template); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"venator-findings-v1*"}, template.IndexPatterns); diff != "" {
		t.Fatalf("index patterns mismatch (-want +got):\n%s", diff)
	}
	payload := template.Template.Mappings.Properties["payload"]
	if template.Template.Mappings.Dynamic || payload.Type != "object" || payload.Enabled == nil || *payload.Enabled {
		t.Fatalf("unsafe payload mapping: dynamic=%v payload=%+v", template.Template.Mappings.Dynamic, payload)
	}
	properties := template.Template.Mappings.Properties
	if properties["detected_at"].Type != "date_nanos" || properties["event_at"].Type != "date_nanos" ||
		properties["review"].Properties["reviewed_at"].Type != "date_nanos" {
		t.Fatal("timestamp mappings must preserve nanosecond precision")
	}
	if properties["source"].IgnoreAbove != 1_024 || properties["rule"].Properties["name"].IgnoreAbove != 1_024 ||
		properties["attributes"].Properties["actor_user_name"].IgnoreAbove != 1_024 {
		t.Fatal("variable keyword mappings must ignore oversized terms")
	}
}

func newTestClient(t *testing.T, handler http.Handler) *opensearch.Client {
	t.Helper()
	return newTestClientWithConfig(t, handler, opensearch.Config{})
}

func newTestClientWithConfig(t *testing.T, handler http.Handler, cfg opensearch.Config) *opensearch.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	cfg.URL = server.URL
	cfg.Username = "user"
	cfg.Password = "password"
	client, err := opensearch.New(context.Background(), cfg)
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
