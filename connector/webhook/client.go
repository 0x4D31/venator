// Package webhook delivers canonical findings to generic HTTP endpoints.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/0x4D31/venator/internal/model"
)

const (
	defaultTimeout         = 20 * time.Second
	defaultMaxAttempts     = 3
	defaultMaxFindings     = 100
	defaultMaxPayloadBytes = 1 << 20

	maxConfiguredAttempts     = 10
	maxConfiguredFindings     = 1_000
	maxConfiguredPayloadBytes = 10 << 20
	maxResponseBytes          = 64 << 10
	maxFindingIDBytes         = 1 << 10
	baseRetryDelay            = 500 * time.Millisecond
	maxRetryDelay             = 30 * time.Second
)

var errResponseTooLarge = errors.New("webhook response exceeds the 65536-byte limit")

var reservedHeaders = map[string]struct{}{
	"accept":              {},
	"connection":          {},
	"content-encoding":    {},
	"content-length":      {},
	"content-type":        {},
	"host":                {},
	"idempotency-key":     {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"te":                  {},
	"trailer":             {},
	"transfer-encoding":   {},
	"upgrade":             {},
	"venator-finding-id":  {},
	"webhook-id":          {},
	"webhook-signature":   {},
	"webhook-timestamp":   {},
}

// Client sends each finding as one JSON request. Its retry hooks are fields so
// package tests can exercise timing without sleeping; production callers only
// construct clients through New.
type Client struct {
	endpoint        string
	headers         http.Header
	signingKey      []byte
	httpClient      *http.Client
	maxAttempts     int
	maxFindings     int
	maxPayloadBytes int
	now             func() time.Time
	wait            func(context.Context, time.Duration) error
	jitter          func(time.Duration) time.Duration
}

type preparedFinding struct {
	body      []byte
	findingID string
	webhookID string
}

// New validates all static request state without contacting the endpoint.
func New(ctx context.Context, cfg Config) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("create webhook connector: %w", err)
	}

	endpoint, err := validateEndpoint(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("create webhook connector: %w", err)
	}
	headers, err := validateHeaders(cfg.Headers)
	if err != nil {
		return nil, fmt.Errorf("create webhook connector: %w", err)
	}
	signingKey, err := decodeSigningSecret(cfg.SigningSecret)
	if err != nil {
		return nil, fmt.Errorf("create webhook connector: %w", err)
	}

	if cfg.Timeout == 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.Timeout < 0 {
		return nil, errors.New("create webhook connector: timeout must be positive")
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = defaultMaxAttempts
	}
	if cfg.MaxAttempts < 1 || cfg.MaxAttempts > maxConfiguredAttempts {
		return nil, fmt.Errorf("create webhook connector: maxAttempts must be between 1 and %d", maxConfiguredAttempts)
	}
	if cfg.MaxFindings == 0 {
		cfg.MaxFindings = defaultMaxFindings
	}
	if cfg.MaxFindings < 1 || cfg.MaxFindings > maxConfiguredFindings {
		return nil, fmt.Errorf("create webhook connector: maxFindings must be between 1 and %d", maxConfiguredFindings)
	}
	if cfg.MaxPayloadBytes == 0 {
		cfg.MaxPayloadBytes = defaultMaxPayloadBytes
	}
	if cfg.MaxPayloadBytes < 1 || cfg.MaxPayloadBytes > maxConfiguredPayloadBytes {
		return nil, fmt.Errorf("create webhook connector: maxPayloadBytes must be between 1 and %d", maxConfiguredPayloadBytes)
	}

	return &Client{
		endpoint:   endpoint,
		headers:    headers,
		signingKey: signingKey,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		maxAttempts:     cfg.MaxAttempts,
		maxFindings:     cfg.MaxFindings,
		maxPayloadBytes: cfg.MaxPayloadBytes,
		now:             time.Now,
		wait:            waitForRetry,
		jitter: func(delay time.Duration) time.Duration {
			half := delay / 2
			return half + time.Duration(rand.Int64N(int64(delay-half)+1))
		},
	}, nil
}

// Publish posts each canonical Finding as the complete request body. Every
// finding is encoded and size-checked before the first request is attempted.
func (c *Client) Publish(ctx context.Context, batch model.PublishBatch) error {
	prepared, err := c.prepare(ctx, batch)
	if err != nil {
		return err
	}
	for i, finding := range prepared {
		if err := c.publishFinding(ctx, finding); err != nil {
			return fmt.Errorf("publish webhook finding %d: %w", i, err)
		}
	}
	return nil
}

func (c *Client) prepare(ctx context.Context, batch model.PublishBatch) ([]preparedFinding, error) {
	if len(batch.Findings) == 0 {
		return nil, nil
	}
	if len(batch.Findings) > c.maxFindings {
		return nil, fmt.Errorf("webhook batch has %d findings; configured limit is %d", len(batch.Findings), c.maxFindings)
	}

	prepared := make([]preparedFinding, 0, len(batch.Findings))
	seenWebhookIDs := make(map[string]struct{}, len(batch.Findings))
	for i, finding := range batch.Findings {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if finding.RunID == "" {
			return nil, fmt.Errorf("preflight webhook finding %d: run_id is required", i)
		}
		if finding.ID == "" {
			return nil, fmt.Errorf("preflight webhook finding %d: finding ID is required", i)
		}
		if len(finding.ID) > maxFindingIDBytes || !validHeaderValue(finding.ID) {
			return nil, fmt.Errorf("preflight webhook finding %d: finding ID is not safe for an HTTP header", i)
		}
		if batch.RunID != "" && batch.RunID != finding.RunID {
			return nil, fmt.Errorf("preflight webhook finding %d: finding run_id does not match batch run_id", i)
		}

		body, err := json.Marshal(finding)
		if err != nil {
			return nil, fmt.Errorf("preflight webhook finding %d: marshal canonical finding: %w", i, err)
		}
		if len(body) > c.maxPayloadBytes {
			return nil, fmt.Errorf("preflight webhook finding %d: payload is %d bytes; configured limit is %d", i, len(body), c.maxPayloadBytes)
		}

		webhookID := findingWebhookID(finding.RunID, finding.ID)
		if _, exists := seenWebhookIDs[webhookID]; exists {
			return nil, fmt.Errorf("preflight webhook finding %d: duplicate run_id and finding ID", i)
		}
		seenWebhookIDs[webhookID] = struct{}{}
		prepared = append(prepared, preparedFinding{body: body, findingID: finding.ID, webhookID: webhookID})
	}
	return prepared, nil
}

func (c *Client) publishFinding(ctx context.Context, finding preparedFinding) error {
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		timestamp := c.now().Unix()

		req, err := c.newRequest(ctx, finding, timestamp)
		if err != nil {
			return fmt.Errorf("create webhook request: %w", err)
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			requestErr := safeRequestError(err)
			if attempt == c.maxAttempts {
				return fmt.Errorf("webhook request failed after %d attempts: %w", attempt, requestErr)
			}
			if err := c.wait(ctx, c.jitter(exponentialRetryDelay(attempt))); err != nil {
				return fmt.Errorf("wait before webhook retry: %w", err)
			}
			continue
		}

		retryAfter, hasRetryAfter := retryAfterDelay(resp.Header.Get("Retry-After"), c.now())
		responseErr := readResponse(resp.Body)

		successful := resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices
		if successful {
			return nil
		}
		attemptErr := responseStatusError(resp.StatusCode)
		if responseErr != nil {
			attemptErr = errors.Join(attemptErr, fmt.Errorf("read webhook response: %w", responseErr))
		}
		if !retryableStatus(resp.StatusCode) {
			return attemptErr
		}

		if attempt == c.maxAttempts {
			return fmt.Errorf("webhook request failed after %d attempts: %w", attempt, attemptErr)
		}
		delay := retryAfter
		if !hasRetryAfter {
			delay = c.jitter(exponentialRetryDelay(attempt))
		}
		if err := c.wait(ctx, delay); err != nil {
			return fmt.Errorf("wait before webhook retry: %w", err)
		}
	}
	panic("unreachable")
}

func (c *Client) newRequest(ctx context.Context, finding preparedFinding, timestamp int64) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(finding.body))
	if err != nil {
		return nil, err
	}
	req.Header = c.headers.Clone()
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", finding.webhookID)
	req.Header.Set("Venator-Finding-ID", finding.findingID)
	req.Header.Set("Webhook-ID", finding.webhookID)
	req.Header.Set("Webhook-Timestamp", strconv.FormatInt(timestamp, 10))
	if len(c.signingKey) > 0 {
		req.Header.Set("Webhook-Signature", sign(c.signingKey, finding.webhookID, timestamp, finding.body))
	}
	return req, nil
}

func validateEndpoint(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", errors.New("webhook URL is invalid")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" ||
		parsed.ForceQuery || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("webhook URL is invalid")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("webhook URL is invalid")
	}
	if scheme == "http" {
		ip := net.ParseIP(parsed.Hostname())
		if ip == nil || !ip.IsLoopback() {
			return "", errors.New("webhook URL must use HTTPS (HTTP is allowed only for a literal loopback address)")
		}
	}
	parsed.Scheme = scheme
	return parsed.String(), nil
}

func validateHeaders(configured map[string]string) (http.Header, error) {
	headers := make(http.Header, len(configured))
	seen := make(map[string]struct{}, len(configured))
	for name, value := range configured {
		lowerName := strings.ToLower(name)
		if !validHeaderName(name) {
			return nil, fmt.Errorf("custom webhook header name %q is invalid", name)
		}
		if _, reserved := reservedHeaders[lowerName]; reserved {
			return nil, fmt.Errorf("custom webhook header %q is reserved", name)
		}
		if _, duplicate := seen[lowerName]; duplicate {
			return nil, fmt.Errorf("custom webhook header %q is duplicated with different casing", name)
		}
		if !validHeaderValue(value) {
			return nil, fmt.Errorf("custom webhook header %q has an invalid value", name)
		}
		seen[lowerName] = struct{}{}
		headers.Set(name, value)
	}
	return headers, nil
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		character := name[i]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(character)) {
			continue
		}
		return false
	}
	return true
}

func validHeaderValue(value string) bool {
	for i := 0; i < len(value); i++ {
		character := value[i]
		if character == '\t' || character >= 0x20 && character != 0x7f {
			continue
		}
		return false
	}
	return true
}

func decodeSigningSecret(secret string) ([]byte, error) {
	if secret == "" {
		return nil, nil
	}
	if strings.TrimSpace(secret) != secret || !strings.HasPrefix(secret, "whsec_") {
		return nil, errors.New("signingSecret must be a whsec_ prefixed base64 value")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil {
		return nil, errors.New("signingSecret must contain valid base64")
	}
	if len(decoded) < 24 || len(decoded) > 64 {
		return nil, errors.New("signingSecret must decode to between 24 and 64 bytes")
	}
	return decoded, nil
}

func findingWebhookID(runID, findingID string) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, runID)
	_, _ = hash.Write([]byte{0})
	_, _ = io.WriteString(hash, findingID)
	return "msg_" + hex.EncodeToString(hash.Sum(nil))
}

func sign(key []byte, webhookID string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, key)
	_, _ = io.WriteString(mac, webhookID)
	_, _ = io.WriteString(mac, ".")
	_, _ = io.WriteString(mac, strconv.FormatInt(timestamp, 10))
	_, _ = io.WriteString(mac, ".")
	_, _ = mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func exponentialRetryDelay(failedAttempt int) time.Duration {
	delay := baseRetryDelay
	for i := 1; i < failedAttempt && delay < maxRetryDelay; i++ {
		delay *= 2
		if delay > maxRetryDelay {
			return maxRetryDelay
		}
	}
	return delay
}

func retryAfterDelay(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
		if seconds > int64(maxRetryDelay/time.Second) {
			return maxRetryDelay, true
		}
		return time.Duration(seconds) * time.Second, true
	}
	retryAt, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	delay := retryAt.Sub(now)
	if delay < 0 {
		delay = 0
	}
	if delay > maxRetryDelay {
		delay = maxRetryDelay
	}
	return delay, true
}

func retryableStatus(statusCode int) bool {
	return statusCode == http.StatusRequestTimeout || statusCode == http.StatusTooEarly ||
		statusCode == http.StatusTooManyRequests || statusCode >= 500 && statusCode <= 599 && statusCode != http.StatusNotImplemented
}

func readResponse(body io.ReadCloser) error {
	responseBody, readErr := io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	closeErr := body.Close()
	if len(responseBody) > maxResponseBytes {
		return errors.Join(errResponseTooLarge, closeErr)
	}
	return errors.Join(readErr, closeErr)
}

func responseStatusError(statusCode int) error {
	status := strconv.Itoa(statusCode)
	if text := http.StatusText(statusCode); text != "" {
		status += " " + text
	}
	return fmt.Errorf("webhook returned %s", status)
}

func safeRequestError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err
	}
	return errors.New("HTTP transport failed")
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
