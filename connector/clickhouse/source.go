package clickhouse

import (
	"context"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/0x4D31/venator/internal/config"
	"github.com/0x4D31/venator/internal/model"
	ch "github.com/ClickHouse/clickhouse-go/v2"
	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// Source executes rule SQL and preserves the ClickHouse driver's native value
// types in model.Record.
type Source struct {
	client   *Client
	timeout  config.Duration
	maxRows  uint64
	maxBytes uint64
}

func NewSource(client *Client) (*Source, error) {
	if err := client.ensureOpen(); err != nil {
		return nil, fmt.Errorf("create ClickHouse source: %w", err)
	}
	if client.cfg.Query == nil {
		return nil, errors.New("create ClickHouse source: query is not configured")
	}
	maxBytes := client.cfg.Query.MaxBytes
	if maxBytes == 0 {
		maxBytes = 64 << 20
	}
	return &Source{
		client:   client,
		timeout:  client.cfg.Query.Timeout,
		maxRows:  client.cfg.Query.MaxRows,
		maxBytes: maxBytes,
	}, nil
}

func querySettings(maxRows, maxBytes uint64) ch.Settings {
	return ch.Settings{
		"max_result_rows":      maxRows,
		"max_result_bytes":     maxBytes,
		"result_overflow_mode": "throw",
	}
}

func (s *Source) Query(ctx context.Context, ruleConfig *config.RuleConfig) (records []model.Record, err error) {
	if s == nil || s.client == nil {
		return nil, errors.New("query ClickHouse: source is not initialized")
	}
	if err := s.client.ensureOpen(); err != nil {
		return nil, fmt.Errorf("query ClickHouse: %w", err)
	}
	if ruleConfig == nil {
		return nil, errors.New("query ClickHouse: rule config is nil")
	}
	query := strings.TrimSpace(ruleConfig.Query)
	if query == "" {
		return nil, errors.New("query ClickHouse: rule query is empty")
	}
	if s.maxRows == 0 {
		return nil, errors.New("query ClickHouse: maxRows must be positive")
	}

	queryCtx, cancel := context.WithTimeout(ctx, s.timeout.Value())
	defer cancel()
	queryCtx = ch.Context(queryCtx, ch.WithSettings(querySettings(s.maxRows, s.maxBytes)))

	rows, err := s.client.conn.Query(queryCtx, query)
	if err != nil {
		return nil, fmt.Errorf("execute ClickHouse query: %w", err)
	}
	if rows == nil {
		return nil, errors.New("execute ClickHouse query: driver returned nil rows")
	}
	defer func() {
		err = errors.Join(err, wrapError("close ClickHouse query rows", rows.Close()))
	}()

	columns := rows.Columns()
	columnTypes := rows.ColumnTypes()
	if err := validateColumns(columns, columnTypes); err != nil {
		return nil, err
	}

	capacity := 256
	if s.maxRows < uint64(capacity) {
		capacity = int(s.maxRows)
	}
	records = make([]model.Record, 0, capacity)
	var totalBytes uint64
	for rows.Next() {
		if uint64(len(records)) >= s.maxRows {
			return nil, fmt.Errorf("ClickHouse query exceeded client row limit %d", s.maxRows)
		}
		targets, err := scanTargets(columnTypes)
		if err != nil {
			return nil, err
		}
		if err := rows.Scan(targets...); err != nil {
			return nil, fmt.Errorf("scan ClickHouse row %d: %w", len(records), err)
		}

		record := make(model.Record, len(columns))
		for i, column := range columns {
			record[column] = scannedValue(targets[i])
		}
		encoded, err := json.Marshal(record)
		if err != nil {
			return nil, fmt.Errorf("measure ClickHouse row %d: %w", len(records), err)
		}
		totalBytes += uint64(len(encoded))
		if totalBytes > s.maxBytes {
			return nil, fmt.Errorf("ClickHouse query exceeded client byte limit %d", s.maxBytes)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ClickHouse query rows: %w", err)
	}
	return records, nil
}

func validateColumns(columns []string, columnTypes []chdriver.ColumnType) error {
	if len(columns) != len(columnTypes) {
		return fmt.Errorf("malformed ClickHouse result: %d column names for %d column types", len(columns), len(columnTypes))
	}
	seen := make(map[string]struct{}, len(columns))
	for i, column := range columns {
		if strings.TrimSpace(column) == "" {
			return fmt.Errorf("malformed ClickHouse result: column %d has no name", i)
		}
		if _, exists := seen[column]; exists {
			return fmt.Errorf("malformed ClickHouse result: duplicate column %q", column)
		}
		seen[column] = struct{}{}
	}
	return nil
}

func scanTargets(columnTypes []chdriver.ColumnType) ([]any, error) {
	targets := make([]any, len(columnTypes))
	for i, columnType := range columnTypes {
		if columnType == nil || columnType.ScanType() == nil {
			return nil, fmt.Errorf("ClickHouse column %d has no scan type", i)
		}
		targets[i] = reflect.New(columnType.ScanType()).Interface()
	}
	return targets, nil
}

func scannedValue(target any) any {
	value := reflect.ValueOf(target)
	if !value.IsValid() || value.Kind() != reflect.Pointer || value.IsNil() {
		return nil
	}
	// Strip the pointer allocated as the Scan destination. A pointer left after
	// this step is either Nullable(T) or a native pointer-valued ClickHouse type
	// such as *big.Int. Preserve pointer types with their own JSON/text encoding;
	// dereference ordinary nullable primitives for ergonomic rule processing.
	value = value.Elem()
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		candidate := value.Interface()
		if _, ok := candidate.(json.Marshaler); ok {
			return candidate
		}
		if _, ok := candidate.(encoding.TextMarshaler); ok {
			return candidate
		}
		value = value.Elem()
	}
	result := value.Interface()
	if bytes, ok := result.([]byte); ok {
		return append([]byte(nil), bytes...)
	}
	return result
}

func (s *Source) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Close()
}
