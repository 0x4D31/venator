// Package stdio provides scheduler- and agent-friendly NDJSON boundaries.
package stdio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/0x4D31/venator/internal/config"
	"github.com/0x4D31/venator/internal/model"
)

const defaultMaxRecordBytes = 4 << 20

type Source struct {
	reader         io.Reader
	maxRecords     int
	maxRecordBytes int
	maxTotalBytes  int64
}

type FileSource struct {
	maxRecords    int
	maxTotalBytes int64
}

type queryResult struct {
	records []model.Record
	err     error
}

type publishResult struct {
	err error
}

func NewSource(reader io.Reader, maxRecords int, maxTotalBytes int64) *Source {
	return &Source{reader: reader, maxRecords: maxRecords, maxRecordBytes: defaultMaxRecordBytes, maxTotalBytes: maxTotalBytes}
}

func NewFileSource(maxRecords int, maxTotalBytes int64) *FileSource {
	return &FileSource{maxRecords: maxRecords, maxTotalBytes: maxTotalBytes}
}

func (s *FileSource) Query(ctx context.Context, rule *config.RuleConfig) ([]model.Record, error) {
	if rule == nil || rule.Query == "" {
		return nil, fmt.Errorf("file NDJSON source requires a path in rule.query")
	}
	file, err := openRegularFile(rule.Query)
	if err != nil {
		return nil, err
	}
	source := NewSource(file, s.maxRecords, s.maxTotalBytes)
	records, queryErr := source.Query(ctx, rule)
	closeErr := file.Close()
	if errors.Is(closeErr, os.ErrClosed) {
		closeErr = nil
	}
	return records, errors.Join(queryErr, closeErr)
}

// ValidateFile verifies the finite-file source without reading its contents.
// Pipes, devices, and sockets belong at stdin.default; rejecting them here
// prevents os.Open from blocking outside the run context.
func ValidateFile(path string) error {
	file, err := openRegularFile(path)
	if err != nil {
		return err
	}
	return errors.Join(file.Close())
}

func openRegularFile(path string) (*os.File, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat NDJSON file %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("NDJSON file %q must be a regular file (use stdin.default for streams)", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open NDJSON file %q: %w", path, err)
	}
	openedInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect NDJSON file %q: %w", path, err)
	}
	if !openedInfo.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("NDJSON file %q changed and is no longer a regular file", path)
	}
	return file, nil
}

func (s *Source) Query(ctx context.Context, _ *config.RuleConfig) ([]model.Record, error) {
	result := make(chan queryResult, 1)
	go func() {
		records, err := s.scan(ctx)
		result <- queryResult{records: records, err: err}
	}()

	select {
	case completed := <-result:
		return completed.records, completed.err
	case <-ctx.Done():
		// Closing a pipe, file, or os.Stdin unblocks a pending Scan. Query still
		// returns promptly for injected readers that do not implement io.Closer.
		if closer, ok := s.reader.(io.Closer); ok {
			_ = closer.Close()
		}
		return nil, ctx.Err()
	}
}

func (s *Source) scan(ctx context.Context) ([]model.Record, error) {
	scanner := bufio.NewScanner(s.reader)
	scanner.Buffer(make([]byte, 64*1024), s.maxRecordBytes)
	records := make([]model.Record, 0)
	line := 0
	var totalBytes int64
	for scanner.Scan() {
		line++
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		if len(scanner.Bytes()) == 0 {
			continue
		}
		totalBytes += int64(len(scanner.Bytes()))
		if s.maxTotalBytes > 0 && totalBytes > s.maxTotalBytes {
			return nil, fmt.Errorf("stdin produced more than %d bytes", s.maxTotalBytes)
		}
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.UseNumber()
		var record model.Record
		if err := decoder.Decode(&record); err != nil {
			return nil, fmt.Errorf("decode NDJSON line %d: %w", line, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			if err == nil {
				return nil, fmt.Errorf("decode NDJSON line %d: multiple JSON values", line)
			}
			return nil, fmt.Errorf("decode NDJSON line %d trailing data: %w", line, err)
		}
		if record == nil {
			return nil, fmt.Errorf("decode NDJSON line %d: expected an object", line)
		}
		records = append(records, record)
		if len(records) > s.maxRecords {
			return nil, fmt.Errorf("stdin produced more than %d records", s.maxRecords)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read NDJSON: %w", err)
	}
	return records, nil
}

type Sink struct {
	writer io.Writer
	mu     sync.Mutex
}

func NewSink(writer io.Writer) *Sink { return &Sink{writer: writer} }

func (s *Sink) Publish(ctx context.Context, batch model.PublishBatch) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	closer, cancellable := s.writer.(io.Closer)
	if !cancellable {
		return s.publish(ctx, batch)
	}
	completed := make(chan publishResult, 1)
	go func() {
		completed <- publishResult{err: s.publish(ctx, batch)}
	}()
	select {
	case result := <-completed:
		return result.err
	case <-ctx.Done():
		// os.Stdout and io.Pipe unblock a pending write when closed. Returning the
		// context error keeps SIGTERM/runtime deadlines truthful for agent runs.
		_ = closer.Close()
		return ctx.Err()
	}
}

func (s *Sink) publish(ctx context.Context, batch model.PublishBatch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	encoder := json.NewEncoder(s.writer)
	encoder.SetEscapeHTML(false)
	for i := range batch.Findings {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := encoder.Encode(batch.Findings[i]); err != nil {
			return fmt.Errorf("encode finding %d: %w", i, err)
		}
	}
	return nil
}
