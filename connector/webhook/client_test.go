package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0x4D31/venator/internal/model"
)

func TestPublishPostsOneExactCanonicalFindingPerRequest(t *testing.T) {
	batch := testBatch()
	second := batch.Findings[0]
	second.ID = "finding-2"
	second.Payload = map[string]any{"event": "logout"}
	batch.Findings = append(batch.Findings, second)

	type capturedRequest struct {
		body   []byte
		header http.Header
	}
	requests := make(chan capturedRequest, len(batch.Findings))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		requests <- capturedRequest{body: body, header: r.Header.Clone()}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server.URL, Config{Headers: map[string]string{"Authorization": "Bearer test-token"}})

	if err := client.Publish(context.Background(), batch); err != nil {
		t.Fatalf("Publish() error: %v", err)
	}

	seenWebhookIDs := make(map[string]struct{})
	for i, finding := range batch.Findings {
		request := <-requests
		wantBody, err := json.Marshal(finding)
		if err != nil {
			t.Fatal(err)
		}
		if string(request.body) != string(wantBody) {
			t.Errorf("request %d body = %s, want exact canonical JSON %s", i, request.body, wantBody)
		}
		if got := request.header.Get("Content-Type"); got != "application/json" {
			t.Errorf("request %d Content-Type = %q", i, got)
		}
		if got := request.header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("request %d Authorization = %q", i, got)
		}
		if got := request.header.Get("Venator-Finding-ID"); got != finding.ID {
			t.Errorf("request %d finding ID = %q, want %q", i, got, finding.ID)
		}
		webhookID := request.header.Get("Webhook-ID")
		if webhookID == "" || request.header.Get("Idempotency-Key") != webhookID {
			t.Errorf("request %d webhook/idempotency ID mismatch", i)
		}
		if _, duplicate := seenWebhookIDs[webhookID]; duplicate {
			t.Errorf("request %d reused webhook ID %q", i, webhookID)
		}
		seenWebhookIDs[webhookID] = struct{}{}
		if got := request.header.Get("Webhook-Signature"); got != "" {
			t.Errorf("unsigned request has signature %q", got)
		}
	}
}

func TestPublishSignsExactBodyAndKeepsIDStableAcrossRetry(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	secret := "whsec_" + base64.StdEncoding.EncodeToString(key)

	type attempt struct {
		body      []byte
		id        string
		timestamp string
		signature string
	}
	attempts := make(chan attempt, 2)
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		attempts <- attempt{
			body:      body,
			id:        r.Header.Get("Webhook-ID"),
			timestamp: r.Header.Get("Webhook-Timestamp"),
			signature: r.Header.Get("Webhook-Signature"),
		}
		if requestCount.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server.URL, Config{SigningSecret: secret})
	client.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	client.wait = func(context.Context, time.Duration) error { return nil }
	client.jitter = func(delay time.Duration) time.Duration { return delay }

	if err := client.Publish(context.Background(), testBatch()); err != nil {
		t.Fatalf("Publish() error: %v", err)
	}
	first, second := <-attempts, <-attempts
	if first.id == "" || first.id != second.id {
		t.Fatalf("webhook IDs = %q, %q; want one stable non-empty ID", first.id, second.id)
	}
	if first.timestamp != "1700000000" || second.timestamp != first.timestamp {
		t.Fatalf("retry timestamps = %q, %q; want actual attempt time", first.timestamp, second.timestamp)
	}
	if string(first.body) != string(second.body) {
		t.Fatal("retry changed the request body")
	}
	for i, got := range []attempt{first, second} {
		timestamp, err := strconv.ParseInt(got.timestamp, 10, 64)
		if err != nil {
			t.Fatalf("attempt %d timestamp: %v", i, err)
		}
		want := standardSignature(key, got.id, timestamp, got.body)
		if got.signature != want {
			t.Errorf("attempt %d signature = %q, want %q", i, got.signature, want)
		}
	}
}

func TestFindingWebhookIDDistinguishesSchedulerRuns(t *testing.T) {
	first := findingWebhookID("run-1", "finding-1")
	retry := findingWebhookID("run-1", "finding-1")
	if first == "" || first != retry {
		t.Fatalf("webhook IDs = %q, %q", first, retry)
	}
	if first == findingWebhookID("run-2", "finding-1") {
		t.Fatal("different runs received the same webhook ID")
	}
	if first == findingWebhookID("run-1", "finding-2") {
		t.Fatal("different findings received the same webhook ID")
	}
}

func TestPublishPreflightsWholeBatchBeforeNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	t.Cleanup(server.Close)

	tests := map[string]func(*model.PublishBatch, *Client){
		"serialization": func(batch *model.PublishBatch, _ *Client) {
			invalid := batch.Findings[0]
			invalid.ID = "finding-2"
			invalid.Payload = make(chan int)
			batch.Findings = append(batch.Findings, invalid)
		},
		"payload limit": func(batch *model.PublishBatch, client *Client) {
			second := batch.Findings[0]
			second.ID = "finding-2"
			second.Payload = strings.Repeat("x", 2_000)
			batch.Findings = append(batch.Findings, second)
			client.maxPayloadBytes = 1_000
		},
		"duplicate identity": func(batch *model.PublishBatch, _ *Client) {
			batch.Findings = append(batch.Findings, batch.Findings[0])
		},
		"unsafe header": func(batch *model.PublishBatch, _ *Client) {
			second := batch.Findings[0]
			second.ID = "finding-2\r\ninjected: true"
			batch.Findings = append(batch.Findings, second)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			requests.Store(0)
			client := newTestClient(t, server.URL, Config{})
			batch := testBatch()
			mutate(&batch, client)
			if err := client.Publish(context.Background(), batch); err == nil {
				t.Fatal("Publish() error = nil, want a preflight error")
			}
			if got := requests.Load(); got != 0 {
				t.Fatalf("server received %d request(s), want 0", got)
			}
		})
	}
}

func TestPublishRetriesOnlyRetryableResponses(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		wantRequests int
	}{
		{name: "408", status: http.StatusRequestTimeout, wantRequests: 3},
		{name: "425", status: http.StatusTooEarly, wantRequests: 3},
		{name: "429", status: http.StatusTooManyRequests, wantRequests: 3},
		{name: "500", status: http.StatusInternalServerError, wantRequests: 3},
		{name: "503", status: http.StatusServiceUnavailable, wantRequests: 3},
		{name: "400", status: http.StatusBadRequest, wantRequests: 1},
		{name: "409", status: http.StatusConflict, wantRequests: 1},
		{name: "501", status: http.StatusNotImplemented, wantRequests: 1},
		{name: "redirect", status: http.StatusFound, wantRequests: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.Header().Set("Location", "https://example.invalid/redirected")
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, "safe detail")
			}))
			t.Cleanup(server.Close)
			client := newTestClient(t, server.URL, Config{})
			client.wait = func(context.Context, time.Duration) error { return nil }
			client.jitter = func(delay time.Duration) time.Duration { return delay }

			err := client.Publish(context.Background(), testBatch())
			if err == nil || !strings.Contains(err.Error(), strconv.Itoa(tt.status)) {
				t.Fatalf("Publish() error = %v, want status %d", err, tt.status)
			}
			if got := int(requests.Load()); got != tt.wantRequests {
				t.Fatalf("request count = %d, want %d", got, tt.wantRequests)
			}
		})
	}
}

func TestPublishHonorsBoundedRetryAfter(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server.URL, Config{})
	var delays []time.Duration
	client.wait = func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	}
	client.jitter = func(time.Duration) time.Duration {
		t.Fatal("jitter must not replace Retry-After")
		return 0
	}

	if err := client.Publish(context.Background(), testBatch()); err != nil {
		t.Fatalf("Publish() error: %v", err)
	}
	if len(delays) != 1 || delays[0] != maxRetryDelay {
		t.Fatalf("retry delays = %v, want [%s]", delays, maxRetryDelay)
	}
}

func TestPublishRetriesTransportErrorsWithoutLeakingURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	client := newTestClient(t, server.URL+"/private-token", Config{MaxAttempts: 2})
	server.Close()
	client.wait = func(context.Context, time.Duration) error { return nil }
	client.jitter = func(delay time.Duration) time.Duration { return delay }

	err := client.Publish(context.Background(), testBatch())
	if err == nil || !strings.Contains(err.Error(), "after 2 attempts") {
		t.Fatalf("Publish() error = %v", err)
	}
	if strings.Contains(err.Error(), "private-token") {
		t.Fatalf("Publish() leaked endpoint URL: %v", err)
	}
}

func TestPublishPreservesCanceledContext(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server.URL, Config{})
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

func TestPublishTreatsTwoHundredResponseAsSuccessWithoutUnboundedRead(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, strings.Repeat("x", maxResponseBytes+1))
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server.URL, Config{})
	client.wait = func(context.Context, time.Duration) error {
		t.Fatal("successful response must not be retried")
		return nil
	}

	if err := client.Publish(context.Background(), testBatch()); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("server received %d requests, want 1", got)
	}
}

func TestPublishRetriesRetryableStatusWithOversizedResponse(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, strings.Repeat("x", maxResponseBytes+1))
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server.URL, Config{MaxAttempts: 2})
	client.wait = func(context.Context, time.Duration) error { return nil }
	client.jitter = func(delay time.Duration) time.Duration { return delay }

	err := client.Publish(context.Background(), testBatch())
	if err == nil || !strings.Contains(err.Error(), "503") || !errors.Is(err, errResponseTooLarge) {
		t.Fatalf("Publish() error = %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("server received %d requests, want 2", got)
	}
}

func TestPublishDoesNotFollowRedirects(t *testing.T) {
	var destinationRequests atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		destinationRequests.Add(1)
	}))
	t.Cleanup(destination.Close)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", destination.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	t.Cleanup(origin.Close)
	client := newTestClient(t, origin.URL, Config{})

	if err := client.Publish(context.Background(), testBatch()); err == nil || !strings.Contains(err.Error(), "307") {
		t.Fatalf("Publish() error = %v, want redirect status", err)
	}
	if got := destinationRequests.Load(); got != 0 {
		t.Fatalf("redirect destination received %d requests", got)
	}
}

func TestNewRejectsUnsafeConfiguration(t *testing.T) {
	secret := "do-not-print"
	invalidURLs := []string{
		"", "example.test/hook", "ftp://example.test/hook", "http://example.test/hook",
		"http://localhost/hook", "https://user:" + secret + "@example.test/hook",
		"https://example.test/hook?token=" + secret, "https://example.test/hook#" + secret,
	}
	for _, endpoint := range invalidURLs {
		t.Run("url_"+endpoint, func(t *testing.T) {
			_, err := New(context.Background(), Config{URL: endpoint})
			if err == nil {
				t.Fatalf("New(%q) error = nil", endpoint)
			}
			if strings.Contains(err.Error(), secret) || endpoint != "" && strings.Contains(err.Error(), endpoint) {
				t.Fatalf("New() leaked endpoint: %v", err)
			}
		})
	}

	for name, headers := range map[string]map[string]string{
		"reserved":       {"Webhook-Signature": "attacker-controlled"},
		"invalid name":   {"Bad Header": "value"},
		"invalid value":  {"X-Test": "value\r\ninjected: true"},
		"case duplicate": {"X-Test": "one", "x-test": "two"},
	} {
		t.Run("header_"+name, func(t *testing.T) {
			_, err := New(context.Background(), Config{URL: "https://example.test/hook", Headers: headers})
			if err == nil {
				t.Fatal("New() error = nil")
			}
		})
	}

	for name, signingSecret := range map[string]string{
		"no prefix":    base64.StdEncoding.EncodeToString([]byte("0123456789abcdef01234567")),
		"bad base64":   "whsec_not-base64!",
		"too short":    "whsec_" + base64.StdEncoding.EncodeToString([]byte("short")),
		"too long":     "whsec_" + base64.StdEncoding.EncodeToString(make([]byte, 65)),
		"space padded": " whsec_" + base64.StdEncoding.EncodeToString([]byte("0123456789abcdef01234567")),
	} {
		t.Run("secret_"+name, func(t *testing.T) {
			_, err := New(context.Background(), Config{URL: "https://example.test/hook", SigningSecret: signingSecret})
			if err == nil {
				t.Fatal("New() error = nil")
			}
		})
	}
}

func TestNewCopiesCallerOwnedConfiguration(t *testing.T) {
	headers := map[string]string{"Authorization": "Bearer original"}
	secretBytes := []byte("0123456789abcdef0123456789abcdef")
	client := newTestClient(t, "https://example.test/hook", Config{
		Headers:       headers,
		SigningSecret: "whsec_" + base64.StdEncoding.EncodeToString(secretBytes),
	})
	headers["Authorization"] = "Bearer changed"
	secretBytes[0] = 'x'
	if got := client.headers.Get("Authorization"); got != "Bearer original" {
		t.Fatalf("client header = %q", got)
	}
	if string(client.signingKey) != "0123456789abcdef0123456789abcdef" {
		t.Fatal("client signing key changed with caller-owned data")
	}
}

func TestRetryAfterDelay(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		value string
		want  time.Duration
		ok    bool
	}{
		{value: "3", want: 3 * time.Second, ok: true},
		{value: now.Add(4 * time.Second).Format(http.TimeFormat), want: 4 * time.Second, ok: true},
		{value: now.Add(-time.Second).Format(http.TimeFormat), want: 0, ok: true},
		{value: "invalid", ok: false},
	} {
		got, ok := retryAfterDelay(test.value, now)
		if got != test.want || ok != test.ok {
			t.Errorf("retryAfterDelay(%q) = (%s, %t), want (%s, %t)", test.value, got, ok, test.want, test.ok)
		}
	}
}

func standardSignature(key []byte, webhookID string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, key)
	_, _ = fmt.Fprintf(mac, "%s.%d.", webhookID, timestamp)
	_, _ = mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func newTestClient(t *testing.T, endpoint string, overrides Config) *Client {
	t.Helper()
	overrides.URL = endpoint
	client, err := New(context.Background(), overrides)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	return client
}

func testBatch() model.PublishBatch {
	detectedAt := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	rule := model.RuleMetadata{ID: "rule-1", Name: "Test rule", Confidence: "high"}
	return model.PublishBatch{
		RunID:      "run-1",
		DetectedAt: detectedAt,
		Source:     "file.ndjson",
		Rule:       rule,
		Findings: []model.Finding{{
			SchemaVersion: model.FindingSchemaVersion,
			ID:            "finding-1",
			RunID:         "run-1",
			DetectedAt:    detectedAt,
			Source:        "file.ndjson",
			OutputFormat:  "signal",
			Rule:          rule,
			Payload:       map[string]any{"event": "login", "success": false},
		}},
	}
}

func TestPublishIsSafeForConcurrentUse(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server.URL, Config{})

	const publishers = 8
	var group sync.WaitGroup
	group.Add(publishers)
	for range publishers {
		go func() {
			defer group.Done()
			if err := client.Publish(context.Background(), testBatch()); err != nil {
				t.Errorf("Publish() error: %v", err)
			}
		}()
	}
	group.Wait()
	if got := requests.Load(); got != publishers {
		t.Fatalf("server received %d requests, want %d", got, publishers)
	}
}
