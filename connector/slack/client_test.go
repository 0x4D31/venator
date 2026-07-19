package slack

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0x4D31/venator/internal/model"
)

func TestPublishSendsCanonicalFindings(t *testing.T) {
	requestPayload := make(chan webhookPayload, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method %q", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q, want application/json", got)
		}
		var payload webhookPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode webhook payload: %v", err)
		}
		requestPayload <- payload
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server.URL)
	batch := testBatch()

	if err := client.Publish(context.Background(), batch); err != nil {
		t.Fatalf("Publish() error: %v", err)
	}

	payload := <-requestPayload
	if !strings.Contains(payload.Text, "1 finding(s)") || !strings.Contains(payload.Text, batch.Rule.Name) {
		t.Fatalf("unexpected summary text %q", payload.Text)
	}
	if len(payload.Attachments) != 1 {
		t.Fatalf("got %d attachments, want 1", len(payload.Attachments))
	}
	attachment := payload.Attachments[0]
	var finding model.Finding
	if err := json.Unmarshal([]byte(attachment.Text), &finding); err != nil {
		t.Fatalf("attachment is not canonical finding JSON: %v\n%s", err, attachment.Text)
	}
	if finding.ID != batch.Findings[0].ID || finding.RunID != batch.Findings[0].RunID || finding.Rule.ID != batch.Findings[0].Rule.ID {
		t.Fatalf("unexpected finding in attachment: %+v", finding)
	}
}

func TestPublishDisablesMarkdownForHostileFindingContent(t *testing.T) {
	requestBody := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requestBody <- body
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server.URL)
	batch := testBatch()
	batch.Rule.Name = "<!channel>"
	batch.Findings[0].Rule.Name = "<!channel>"
	batch.Findings[0].Payload = model.Record{"message": "``` <!channel> https://attacker.invalid"}
	if err := client.Publish(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	body := string(<-requestBody)
	if !strings.Contains(body, `"mrkdwn":false`) || strings.Contains(body, "mrkdwn_in") {
		t.Fatalf("markdown was not disabled: %s", body)
	}
}

func TestPublishRejectsBatchAboveConfiguredLimit(t *testing.T) {
	client := newTestClient(t, "https://hooks.slack.test/services/token")
	batch := testBatch()
	batch.Findings = append(batch.Findings, batch.Findings[0])
	client.maxFindings = 1
	if err := client.Publish(context.Background(), batch); err == nil || !strings.Contains(err.Error(), "configured limit") {
		t.Fatalf("error = %v", err)
	}
}

func TestTransportErrorDoesNotLeakWebhookToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	client := newTestClient(t, server.URL+"/services/private-token")
	server.Close()
	err := client.Publish(context.Background(), testBatch())
	if err == nil || strings.Contains(err.Error(), "private-token") {
		t.Fatalf("error = %v", err)
	}
}

func TestPublishReportsWebhookError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, "rate_limited")
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server.URL)

	err := client.Publish(context.Background(), testBatch())
	if err == nil {
		t.Fatal("Publish() error = nil, want an error")
	}
	for _, want := range []string{"429 Too Many Requests", "rate_limited"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Publish() error = %q, want substring %q", err, want)
		}
	}
}

func TestPublishHonorsCanceledContext(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server.URL)
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

func TestPublishReturnsSerializationErrorBeforeNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server.URL)
	batch := testBatch()
	batch.Findings[0].Payload = make(chan int)

	err := client.Publish(context.Background(), batch)
	if err == nil || !strings.Contains(err.Error(), "unsupported type") {
		t.Fatalf("Publish() error = %v, want serialization error", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("server received %d request(s), want 0", got)
	}
}

func TestPublishEmptyBatchDoesNotSendRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server.URL)

	if err := client.Publish(context.Background(), model.PublishBatch{}); err != nil {
		t.Fatalf("Publish() error: %v", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("server received %d request(s), want 0", got)
	}
}

func TestNewRejectsInvalidWebhookURL(t *testing.T) {
	for _, webhookURL := range []string{"", "hooks.slack.test/path", "ftp://hooks.slack.test/path"} {
		t.Run(webhookURL, func(t *testing.T) {
			_, err := New(context.Background(), Config{WebhookURL: webhookURL})
			if err == nil {
				t.Fatalf("New(%q) error = nil, want an error", webhookURL)
			}
		})
	}
}

func newTestClient(t *testing.T, webhookURL string) *Client {
	t.Helper()
	client, err := New(context.Background(), Config{WebhookURL: webhookURL})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
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
			Payload:       map[string]any{"event": "login", "success": false},
		}},
	}
}
