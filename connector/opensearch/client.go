package opensearch

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	opensearchclient "github.com/opensearch-project/opensearch-go/v2"

	"github.com/0x4D31/venator/internal/config"
	"github.com/0x4D31/venator/internal/model"
)

const (
	outputIndexName       = "signals"
	sqlPluginPath         = "/_plugins/_sql"
	pplPluginPath         = "/_plugins/_ppl"
	defaultHTTPTimeout    = 2 * time.Minute
	defaultMaxQueryRows   = 10_000
	maxErrorResponseSize  = 64 << 10
	maxQueryResponseSize  = 32 << 20
	maxBulkResponseSize   = 16 << 20
	maxDiagnosticSize     = 2 << 10
	maxReportedItemErrors = 20
	maxBulkRequestSize    = 5 << 20
	maxBulkChunkFindings  = 500
)

type Client struct {
	osConfig    Config
	osClient    *opensearchclient.Client
	httpClient  *http.Client
	osTransport *http.Transport
	maxRows     int
	maxBytes    int64
}

func New(ctx context.Context, cfg Config) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("create OpenSearch client: %w", err)
	}

	endpoint := strings.TrimRight(strings.TrimSpace(cfg.URL), "/")
	parsedURL, err := url.Parse(endpoint)
	if err != nil || parsedURL.Host == "" || parsedURL.User != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return nil, fmt.Errorf("invalid OpenSearch URL %q", cfg.URL)
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{ // #nosec G402 -- explicitly controlled for self-hosted clusters.
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	}
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   defaultHTTPTimeout,
	}
	osc, err := opensearchclient.NewClient(opensearchclient.Config{
		Addresses: []string{endpoint},
		Username:  cfg.Username,
		Password:  cfg.Password,
		Transport: transport,
	})
	if err != nil {
		return nil, fmt.Errorf("create OpenSearch client: %w", err)
	}

	cfg.URL = endpoint
	if cfg.MaxRows <= 0 {
		cfg.MaxRows = defaultMaxQueryRows
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = 64 << 20
	}
	return &Client{
		osClient:    osc,
		osConfig:    cfg,
		httpClient:  httpClient,
		osTransport: transport,
		maxRows:     cfg.MaxRows,
		maxBytes:    cfg.MaxBytes,
	}, nil
}

func (c *Client) Close() error {
	c.osTransport.CloseIdleConnections()
	return nil
}

func (c *Client) Query(ctx context.Context, cfg *config.RuleConfig) ([]model.Record, error) {
	if cfg == nil {
		return nil, errors.New("execute OpenSearch query: rule config is required")
	}
	path, err := queryPath(cfg.Language)
	if err != nil {
		return nil, err
	}
	query := strings.TrimSpace(cfg.Query)
	if query == "" {
		return nil, errors.New("execute OpenSearch query: query is empty")
	}

	var records []model.Record
	var schema []map[string]string
	activeCursor := ""
	var responseBytes int64
	defer func() {
		if activeCursor != "" {
			closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = c.closeCursor(closeCtx, path, activeCursor)
		}
	}()
	request := map[string]string{"query": query}
	for {
		response, pageBytes, err := c.queryPage(ctx, path, request)
		if err != nil {
			return nil, err
		}
		// Track the cursor before validating the page so malformed schemas or rows
		// still trigger best-effort server-side cursor cleanup.
		activeCursor = response.Cursor
		responseBytes += int64(pageBytes)
		if responseBytes > c.maxBytes {
			return nil, fmt.Errorf("OpenSearch query responses exceeded client byte limit %d", c.maxBytes)
		}
		if len(response.Schema) > 0 {
			if len(schema) == 0 {
				schema = response.Schema
			} else if !sameSchema(schema, response.Schema) {
				return nil, errors.New("malformed OpenSearch query response: schema changed between cursor pages")
			}
		}
		if len(schema) == 0 && len(response.Datarows) > 0 {
			return nil, errors.New("malformed OpenSearch query response: rows returned without schema")
		}
		page, err := recordsFromRows(schema, response.Datarows, len(records))
		if err != nil {
			return nil, err
		}
		records = append(records, page...)
		if len(records) > c.maxRows {
			return nil, fmt.Errorf("OpenSearch query exceeded client row limit %d", c.maxRows)
		}
		if activeCursor == "" {
			return records, nil
		}
		request = map[string]string{"cursor": activeCursor}
	}
}

func queryPath(language string) (string, error) {
	switch strings.ToUpper(strings.TrimSpace(language)) {
	case "", "SQL":
		return sqlPluginPath, nil
	case "PPL":
		return pplPluginPath, nil
	default:
		return "", fmt.Errorf("execute OpenSearch query: unsupported language %q", language)
	}
}

func (c *Client) queryPage(ctx context.Context, path string, payload map[string]string) (*QueryResponse, int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, fmt.Errorf("encode OpenSearch query: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.osConfig.URL+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("create OpenSearch query request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	c.setBasicAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("execute OpenSearch query: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, 0, queryResponseError(resp)
	}

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxQueryResponseSize+1))
	if err != nil {
		return nil, 0, fmt.Errorf("read OpenSearch query response: %w", err)
	}
	if len(responseBody) > maxQueryResponseSize {
		return nil, 0, fmt.Errorf("OpenSearch query response exceeds %d bytes", maxQueryResponseSize)
	}
	var response QueryResponse
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	decoder.UseNumber()
	if err := decoder.Decode(&response); err != nil {
		return nil, 0, fmt.Errorf("decode OpenSearch query response: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, 0, fmt.Errorf("decode OpenSearch query response: %w", err)
	}
	if response.Status != 0 && (response.Status < http.StatusOK || response.Status >= http.StatusMultipleChoices) {
		return nil, 0, fmt.Errorf("OpenSearch query response reported status %d", response.Status)
	}

	return &response, len(responseBody), nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("response contains trailing JSON")
}

func recordsFromRows(schema []map[string]string, rows [][]any, offset int) ([]model.Record, error) {
	results := make([]model.Record, 0, len(rows))
	for rowIndex, row := range rows {
		if len(row) != len(schema) {
			return nil, fmt.Errorf("malformed OpenSearch query response: row %d has %d values for %d columns", rowIndex+offset, len(row), len(schema))
		}

		record := make(model.Record, len(schema))
		for columnIndex, column := range schema {
			name := column["alias"]
			if name == "" {
				name = column["name"]
			}
			if name == "" {
				return nil, fmt.Errorf("malformed OpenSearch query response: column %d has no name", columnIndex)
			}
			if _, exists := record[name]; exists {
				return nil, fmt.Errorf("malformed OpenSearch query response: duplicate column %q", name)
			}
			record[name] = row[columnIndex]
		}
		results = append(results, record)
	}

	return results, nil
}

func sameSchema(left, right []map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i]["name"] != right[i]["name"] || left[i]["alias"] != right[i]["alias"] {
			return false
		}
	}
	return true
}

func (c *Client) closeCursor(ctx context.Context, path, cursor string) error {
	body, err := json.Marshal(map[string]string{"cursor": cursor})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.osConfig.URL+path+"/close", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setBasicAuth(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorResponseSize))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("close OpenSearch cursor returned %s", resp.Status)
	}
	return nil
}

func (c *Client) setBasicAuth(req *http.Request) {
	if c.osConfig.Username != "" || c.osConfig.Password != "" {
		req.SetBasicAuth(c.osConfig.Username, c.osConfig.Password)
	}
}

func queryResponseError(resp *http.Response) error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorResponseSize))
	if err != nil {
		return fmt.Errorf("OpenSearch query returned %s and its error body could not be read: %w", resp.Status, err)
	}

	detail := safeDiagnostic(string(body))
	var queryError QueryErrorResponse
	if err := json.Unmarshal(body, &queryError); err == nil {
		parts := make([]string, 0, 2)
		if queryError.Error.Reason != "" {
			parts = append(parts, queryError.Error.Reason)
		}
		if queryError.Error.Details != "" {
			parts = append(parts, queryError.Error.Details)
		}
		if len(parts) > 0 {
			detail = safeDiagnostic(strings.Join(parts, ": "))
		}
	}
	if detail == "" {
		detail = http.StatusText(resp.StatusCode)
	}
	return fmt.Errorf("OpenSearch query returned %s: %s", resp.Status, detail)
}

func (c *Client) Publish(ctx context.Context, batch model.PublishBatch) error {
	if len(batch.Findings) == 0 {
		return nil
	}

	chunks, err := buildBulkRequestChunks(ctx, batch.Findings)
	if err != nil {
		return err
	}
	for i, chunk := range chunks {
		if err := c.publishBulkChunk(ctx, chunk); err != nil {
			return fmt.Errorf("OpenSearch bulk chunk %d of %d: %w", i+1, len(chunks), err)
		}
	}
	return nil
}

type bulkChunk struct {
	body         string
	findingCount int
}

func (c *Client) publishBulkChunk(ctx context.Context, chunk bulkChunk) error {
	requestCtx, cancel := context.WithTimeout(ctx, defaultHTTPTimeout)
	defer cancel()
	bulkResp, err := c.osClient.Bulk(
		strings.NewReader(chunk.body),
		c.osClient.Bulk.WithContext(requestCtx),
	)
	if err != nil {
		return fmt.Errorf("publish OpenSearch bulk request: %w", err)
	}
	if bulkResp == nil || bulkResp.Body == nil {
		return errors.New("publish OpenSearch bulk request: empty response")
	}

	responseBody, readErr := io.ReadAll(io.LimitReader(bulkResp.Body, maxBulkResponseSize+1))
	closeErr := bulkResp.Body.Close()
	if readErr != nil {
		return errors.Join(fmt.Errorf("read OpenSearch bulk response: %w", readErr), closeErr)
	}
	if len(responseBody) > maxBulkResponseSize {
		return errors.Join(fmt.Errorf("OpenSearch bulk response exceeds %d bytes", maxBulkResponseSize), closeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close OpenSearch bulk response: %w", closeErr)
	}
	if bulkResp.IsError() {
		detail := safeDiagnostic(string(responseBody))
		if detail == "" {
			detail = http.StatusText(bulkResp.StatusCode)
		}
		return fmt.Errorf("OpenSearch bulk request returned %s: %s", bulkResp.Status(), detail)
	}

	var response BulkQueryResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return fmt.Errorf("decode OpenSearch bulk response: %w", err)
	}
	if len(response.Items) != chunk.findingCount {
		return fmt.Errorf("OpenSearch bulk response contains %d items for %d findings", len(response.Items), chunk.findingCount)
	}

	var itemErrors []error
	failureCount := 0
	for itemIndex, item := range response.Items {
		if len(item) != 1 {
			failureCount++
			if len(itemErrors) < maxReportedItemErrors {
				itemErrors = append(itemErrors, fmt.Errorf("item %d has %d actions", itemIndex, len(item)))
			}
			continue
		}
		for action, result := range item {
			hasErrorDetail := len(result.Error) > 0 && string(result.Error) != "null"
			if result.Status < http.StatusOK || result.Status >= http.StatusMultipleChoices || hasErrorDetail {
				failureCount++
				detail := safeDiagnostic(string(result.Error))
				if detail == "" {
					detail = http.StatusText(result.Status)
				}
				if len(itemErrors) < maxReportedItemErrors {
					itemErrors = append(itemErrors, fmt.Errorf("item %d %s returned status %d: %s", itemIndex, action, result.Status, detail))
				}
			}
		}
	}
	if response.Errors && len(itemErrors) == 0 {
		itemErrors = append(itemErrors, errors.New("response reported errors without item details"))
	}
	if failureCount > len(itemErrors) {
		itemErrors = append(itemErrors, fmt.Errorf("%d additional item failures omitted", failureCount-len(itemErrors)))
	}
	if len(itemErrors) > 0 {
		return fmt.Errorf("OpenSearch bulk request failed: %w", errors.Join(itemErrors...))
	}

	return nil
}

func safeDiagnostic(value string) string {
	if len(value) > maxDiagnosticSize {
		value = value[:maxDiagnosticSize]
	}
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, value))
}

func buildBulkRequestChunks(ctx context.Context, findings []model.Finding) ([]bulkChunk, error) {
	chunks := make([]bulkChunk, 0, (len(findings)+maxBulkChunkFindings-1)/maxBulkChunkFindings)
	var body bytes.Buffer
	chunkCount := 0
	for findingIndex, finding := range findings {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if finding.ID == "" {
			return nil, fmt.Errorf("build OpenSearch bulk request: finding %d has no ID", findingIndex)
		}
		var item bytes.Buffer
		encoder := json.NewEncoder(&item)
		action := BulkRequestOp{
			Index: &IndexReq{
				Index: outputIndexName,
				ID:    finding.ID,
			},
		}
		if err := encoder.Encode(action); err != nil {
			return nil, fmt.Errorf("encode OpenSearch bulk action for finding %q: %w", finding.ID, err)
		}
		if err := encoder.Encode(finding); err != nil {
			return nil, fmt.Errorf("encode OpenSearch finding %q: %w", finding.ID, err)
		}
		if item.Len() > maxBulkRequestSize {
			return nil, fmt.Errorf("OpenSearch finding %q requires %d bulk bytes; per-request limit is %d", finding.ID, item.Len(), maxBulkRequestSize)
		}
		if chunkCount > 0 && (body.Len()+item.Len() > maxBulkRequestSize || chunkCount >= maxBulkChunkFindings) {
			chunks = append(chunks, bulkChunk{body: body.String(), findingCount: chunkCount})
			body.Reset()
			chunkCount = 0
		}
		_, _ = body.Write(item.Bytes())
		chunkCount++
	}
	if chunkCount > 0 {
		chunks = append(chunks, bulkChunk{body: body.String(), findingCount: chunkCount})
	}
	return chunks, nil
}
