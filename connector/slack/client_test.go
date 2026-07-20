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

func TestPublishSendsDeterministicHumanSummary(t *testing.T) {
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
	if payload.Text != "Venator generated 1 finding(s)." {
		t.Fatalf("unexpected summary text %q", payload.Text)
	}
	if len(payload.Blocks) != 1 {
		t.Fatalf("got %d blocks, want 1", len(payload.Blocks))
	}
	block := payload.Blocks[0]
	if block.Type != "section" || block.Text.Type != "plain_text" {
		t.Fatalf("unexpected block: %+v", block)
	}
	want := strings.Join([]string{
		"Rule: Test rule (rule-1)",
		"Finding ID: finding-1",
		"Source: opensearch.logs",
		"Detected at: 2026-07-18T12:00:00Z",
		"Event at: not set",
		"Verdict: not reviewed",
		`Payload preview: {"event":"login","success":false}`,
	}, "\n")
	if block.Text.Text != want {
		t.Fatalf("summary = %q, want %q", block.Text.Text, want)
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
	batch.Findings[0].Attributes.Message = "``` <!channel> https://attacker.invalid"
	if err := client.Publish(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	body := string(<-requestBody)
	for _, forbidden := range []string{`"mrkdwn"`, `"attachments"`, `"username"`, `"icon_emoji"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("payload contains legacy or Markdown field %s: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, `"type":"plain_text"`) {
		t.Fatalf("payload does not use plain_text blocks: %s", body)
	}
	if strings.Contains(body, `"text":"Venator generated 1 finding(s) by`) {
		t.Fatalf("fallback text contains rule-controlled content: %s", body)
	}
}

func TestPublishBoundsBlockTextByUnicodeCharacters(t *testing.T) {
	requestPayload := make(chan webhookPayload, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload webhookPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		requestPayload <- payload
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server.URL)
	batch := testBatch()
	batch.Findings[0].Attributes.Message = strings.Repeat("🛡", maxBlockTextRunes)
	if err := client.Publish(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	text := (<-requestPayload).Blocks[0].Text.Text
	if got := len([]rune(text)); got > maxBlockTextRunes || !strings.HasSuffix(text, "…") || !strings.Contains(text, "Message: ") {
		t.Fatalf("block text has %d runes and unexpected content %q", got, text)
	}
}

func TestFindingSummaryPrefersMessageAndIncludesReviewVerdict(t *testing.T) {
	finding := testBatch().Findings[0]
	eventAt := time.Date(2026, time.July, 18, 11, 59, 59, 123, time.FixedZone("test", 2*60*60))
	finding.EventAt = &eventAt
	finding.Attributes.Message = "deterministic message"
	finding.Review = &model.Review{Verdict: "suspicious", Reason: "unexpected administrator login"}

	summary, err := findingSummary(finding)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Event at: 2026-07-18T09:59:59.000000123Z",
		"Verdict: suspicious",
		"Review reason: unexpected administrator login",
		"Message: deterministic message",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary %q does not contain %q", summary, want)
		}
	}
	if strings.Contains(summary, "Payload preview:") {
		t.Errorf("summary unexpectedly contains payload preview: %q", summary)
	}
}

func TestFindingSummaryPreservesValidIdentityAtMaximumContent(t *testing.T) {
	finding := testBatch().Findings[0]
	finding.ID = strings.Repeat("a", 64)
	finding.Rule.ID = "6722b4ed-f891-4906-a4b2-f57762dfc72b"
	finding.Rule.Name = strings.Repeat("r", maxSummaryValueRunes)
	finding.Source = strings.Repeat("s", maxSummaryValueRunes)
	finding.Attributes.Message = strings.Repeat("m", maxEvidenceRunes)
	finding.Review = &model.Review{
		Verdict: strings.Repeat("v", maxSummaryValueRunes),
		Reason:  strings.Repeat("x", maxReviewReasonRunes),
	}

	summary, err := findingSummary(finding)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "Finding ID: "+finding.ID) || !strings.Contains(summary, "("+finding.Rule.ID+")") {
		t.Fatalf("summary truncated a valid identity field: %q", summary)
	}
	if got := len([]rune(summary)); got > maxBlockTextRunes {
		t.Fatalf("summary has %d runes, limit is %d", got, maxBlockTextRunes)
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
	secret := "do-not-print"
	for _, webhookURL := range []string{
		"", "hooks.slack.test/path", "ftp://hooks.slack.test/path",
		"https://user:" + secret + "@hooks.slack.test/path",
		"https://hooks.slack.test/path?token=" + secret,
		"https://hooks.slack.test/path#" + secret,
	} {
		t.Run(webhookURL, func(t *testing.T) {
			_, err := New(context.Background(), Config{WebhookURL: webhookURL})
			if err == nil {
				t.Fatalf("New(%q) error = nil, want an error", webhookURL)
			}
			if strings.Contains(err.Error(), secret) || (webhookURL != "" && strings.Contains(err.Error(), webhookURL)) {
				t.Fatalf("New() leaked webhook URL: %v", err)
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
