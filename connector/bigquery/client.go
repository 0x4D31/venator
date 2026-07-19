package bigquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/bigquery"
	"github.com/0x4D31/venator/internal/config"
	"github.com/0x4D31/venator/internal/model"
	"google.golang.org/api/iterator"
)

type Client struct {
	client         *bigquery.Client
	inserter       *bigquery.Inserter
	maxRows        int
	maxBytesBilled int64
	maxResultBytes int64
}

func New(ctx context.Context, config Config) (*Client, error) {
	var inserter *bigquery.Inserter
	client, err := bigquery.NewClient(ctx, config.ProjectID)
	if err != nil {
		return nil, err
	}
	if config.DatasetID != "" && config.TableID != "" {
		// Check if the dataset and table exist
		if _, err := client.Dataset(config.DatasetID).Metadata(ctx); err != nil {
			return nil, errors.Join(fmt.Errorf("read BigQuery dataset metadata: %w", err), client.Close())
		}
		if _, err := client.Dataset(config.DatasetID).Table(config.TableID).Metadata(ctx); err != nil {
			return nil, errors.Join(fmt.Errorf("read BigQuery table metadata: %w", err), client.Close())
		}
		inserter = client.Dataset(config.DatasetID).Table(config.TableID).Inserter()
	}

	return &Client{
		client:         client,
		inserter:       inserter,
		maxRows:        config.MaxRows,
		maxBytesBilled: config.MaxBytesBilled,
		maxResultBytes: config.MaxResultBytes,
	}, nil
}

func (c *Client) Query(ctx context.Context, cfg *config.RuleConfig) ([]model.Record, error) {
	if c == nil || c.client == nil {
		return nil, errors.New("bigquery client is not initialized")
	}
	if cfg == nil {
		return nil, errors.New("bigquery query requires a rule config")
	}
	query := c.client.Query(cfg.Query)
	query.MaxBytesBilled = c.maxBytesBilled
	it, err := query.Read(ctx)
	if err != nil {
		return nil, err
	}

	var results []model.Record
	var resultBytes int64
	for {
		var row map[string]bigquery.Value
		err := it.Next(&row)
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, err
		}
		if c.maxRows > 0 && len(results) >= c.maxRows {
			return nil, fmt.Errorf("bigquery query exceeded client row limit %d", c.maxRows)
		}
		res := make(model.Record)
		for k, v := range row {
			res[k] = v
		}
		encoded, err := json.Marshal(res)
		if err != nil {
			return nil, fmt.Errorf("measure bigquery row %d: %w", len(results), err)
		}
		resultBytes += int64(len(encoded))
		if c.maxResultBytes > 0 && resultBytes > c.maxResultBytes {
			return nil, fmt.Errorf("bigquery query exceeded client byte limit %d", c.maxResultBytes)
		}
		results = append(results, res)
	}

	return results, nil
}

func (c *Client) Publish(ctx context.Context, batch model.PublishBatch) error {
	if len(batch.Findings) == 0 {
		return nil
	}
	if c.inserter == nil {
		return fmt.Errorf("bigquery publisher is not configured with datasetID and tableID")
	}
	rows, err := buildRows(ctx, batch)
	if err != nil {
		return err
	}
	if err := c.inserter.Put(ctx, rows); err != nil {
		return fmt.Errorf("insert %d findings: %w", len(rows), err)
	}
	return nil
}

func buildRows(ctx context.Context, batch model.PublishBatch) ([]*bigquery.StructSaver, error) {
	rows := make([]*bigquery.StructSaver, 0, len(batch.Findings))
	for i, finding := range batch.Findings {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		payload, err := json.Marshal(finding)
		if err != nil {
			return nil, fmt.Errorf("marshal finding %d: %w", i, err)
		}
		row := bigQueryFinding{
			DetectedAt: finding.DetectedAt, EventAt: finding.EventAt, RunID: finding.RunID,
			FindingID: finding.ID, RuleID: finding.Rule.ID, RuleName: finding.Rule.Name,
			Confidence: finding.Rule.Confidence, Source: finding.Source, Payload: string(payload),
		}
		rows = append(rows, &bigquery.StructSaver{Struct: row, InsertID: finding.ID})
	}
	return rows, nil
}

type bigQueryFinding struct {
	DetectedAt time.Time  `bigquery:"detected_at"`
	EventAt    *time.Time `bigquery:"event_at"`
	RunID      string     `bigquery:"run_id"`
	FindingID  string     `bigquery:"finding_id"`
	RuleID     string     `bigquery:"rule_id"`
	RuleName   string     `bigquery:"rule_name"`
	Confidence string     `bigquery:"confidence"`
	Source     string     `bigquery:"source"`
	Payload    string     `bigquery:"payload"`
}

func (c *Client) Close() error { return c.client.Close() }
