package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/0x4D31/venator/internal/model"
)

const (
	defaultHTTPTimeout    = 15 * time.Second
	maxResponseSize       = 64 << 10
	maxErrorDetailSize    = 1024
	maxWebhookPayloadSize = 512 << 10
	defaultMaxFindings    = 20
	maxMessageBlocks      = 50
	maxBlockTextRunes     = 3_000
	maxSummaryValueRunes  = 256
	maxIdentityRunes      = 128
	maxReviewReasonRunes  = 512
	maxEvidenceRunes      = 1_200
)

type Client struct {
	webhookURL  string
	httpClient  *http.Client
	maxFindings int
}

type webhookPayload struct {
	Text   string         `json:"text"`
	Blocks []webhookBlock `json:"blocks"`
}

type webhookBlock struct {
	Type string          `json:"type"`
	Text plainTextObject `json:"text"`
}

type plainTextObject struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func New(ctx context.Context, cfg Config) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("create Slack connector: %w", err)
	}
	webhookURL := strings.TrimSpace(cfg.WebhookURL)
	parsedURL, err := url.Parse(webhookURL)
	if err != nil || parsedURL.Host == "" || parsedURL.User != nil || parsedURL.ForceQuery || parsedURL.RawQuery != "" || parsedURL.Fragment != "" ||
		(parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return nil, errors.New("invalid Slack webhook URL")
	}
	if parsedURL.Scheme != "https" && parsedURL.Hostname() != "localhost" && parsedURL.Hostname() != "127.0.0.1" && parsedURL.Hostname() != "::1" {
		return nil, fmt.Errorf("slack webhook URL must use HTTPS (HTTP is allowed only on loopback)")
	}
	if cfg.MaxFindings == 0 {
		cfg.MaxFindings = defaultMaxFindings
	}
	if cfg.MaxFindings < 1 || cfg.MaxFindings > maxMessageBlocks {
		return nil, fmt.Errorf("slack maxFindings must be between 1 and %d", maxMessageBlocks)
	}
	return &Client{
		webhookURL: webhookURL,
		httpClient: &http.Client{
			Timeout:       defaultHTTPTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		maxFindings: cfg.MaxFindings,
	}, nil
}

func (c *Client) Publish(ctx context.Context, batch model.PublishBatch) error {
	if len(batch.Findings) == 0 {
		return nil
	}
	if len(batch.Findings) > c.maxFindings {
		return fmt.Errorf("slack batch has %d findings; configured limit is %d", len(batch.Findings), c.maxFindings)
	}

	payload, err := buildWebhookPayload(ctx, batch)
	if err != nil {
		return err
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal Slack webhook payload: %w", err)
	}
	if len(payloadBytes) > maxWebhookPayloadSize {
		return fmt.Errorf("slack webhook payload is %d bytes; limit is %d", len(payloadBytes), maxWebhookPayloadSize)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.webhookURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return fmt.Errorf("create Slack webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/plain")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("send Slack webhook: %w", ctxErr)
		}
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return fmt.Errorf("send Slack webhook: %w", urlErr.Err)
		}
		return fmt.Errorf("send Slack webhook failed")
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	closeErr := resp.Body.Close()
	if readErr != nil {
		return errors.Join(fmt.Errorf("read Slack webhook response: %w", readErr), closeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close Slack webhook response: %w", closeErr)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		detail := safeErrorDetail(body)
		if detail == "" {
			detail = http.StatusText(resp.StatusCode)
		}
		return fmt.Errorf("slack webhook returned %s: %s", resp.Status, detail)
	}

	return nil
}

func buildWebhookPayload(ctx context.Context, batch model.PublishBatch) (webhookPayload, error) {
	blocks := make([]webhookBlock, 0, len(batch.Findings))
	for i, finding := range batch.Findings {
		if err := ctx.Err(); err != nil {
			return webhookPayload{}, err
		}
		summary, err := findingSummary(finding)
		if err != nil {
			return webhookPayload{}, fmt.Errorf("summarize Slack finding %d: %w", i, err)
		}

		blocks = append(blocks, webhookBlock{
			Type: "section",
			Text: plainTextObject{
				Type: "plain_text",
				Text: summary,
			},
		})
	}

	return webhookPayload{
		// Keep the notification fallback free of rule-controlled text. Slack may
		// interpret top-level text even though every visible block is plain_text.
		Text:   fmt.Sprintf("Venator generated %d finding(s).", len(batch.Findings)),
		Blocks: blocks,
	}, nil
}

func findingSummary(finding model.Finding) (string, error) {
	payload, err := json.Marshal(finding.Payload)
	if err != nil {
		return "", fmt.Errorf("marshal payload preview: %w", err)
	}

	ruleName := summaryValue(finding.Rule.Name, "unknown")
	ruleID := boundedSummaryValue(finding.Rule.ID, "unknown", maxIdentityRunes)
	verdict := "not reviewed"
	reviewReason := ""
	if finding.Review != nil {
		verdict = summaryValue(finding.Review.Verdict, "unspecified")
		reviewReason = boundedSummaryValue(finding.Review.Reason, "", maxReviewReasonRunes)
	}
	eventAt := "not set"
	if finding.EventAt != nil && !finding.EventAt.IsZero() {
		eventAt = finding.EventAt.UTC().Format(time.RFC3339Nano)
	}
	detectedAt := "unknown"
	if !finding.DetectedAt.IsZero() {
		detectedAt = finding.DetectedAt.UTC().Format(time.RFC3339Nano)
	}

	lines := []string{
		fmt.Sprintf("Rule: %s (%s)", ruleName, ruleID),
		"Finding ID: " + boundedSummaryValue(finding.ID, "unknown", maxIdentityRunes),
		"Source: " + summaryValue(finding.Source, "unknown"),
		"Detected at: " + detectedAt,
		"Event at: " + eventAt,
		"Verdict: " + verdict,
	}
	if reviewReason != "" {
		lines = append(lines, "Review reason: "+reviewReason)
	}
	if message := strings.TrimSpace(finding.Attributes.Message); message != "" {
		lines = append(lines, "Message: "+truncatePlainText(sanitizePlainText(message), maxEvidenceRunes))
	} else {
		lines = append(lines, "Payload preview: "+truncatePlainText(sanitizePlainText(string(payload)), maxEvidenceRunes))
	}
	return truncatePlainText(strings.Join(lines, "\n"), maxBlockTextRunes), nil
}

func summaryValue(value, fallback string) string {
	return boundedSummaryValue(value, fallback, maxSummaryValueRunes)
}

func boundedSummaryValue(value, fallback string, limit int) string {
	value = sanitizePlainText(value)
	if value == "" {
		return fallback
	}
	return truncatePlainText(value, limit)
}

func safeErrorDetail(body []byte) string {
	if len(body) > maxErrorDetailSize {
		body = body[:maxErrorDetailSize]
	}
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, string(body)))
}

func sanitizePlainText(value string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, value))
}

func truncatePlainText(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}
