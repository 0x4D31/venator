package stdio

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0x4D31/venator/internal/config"
	"github.com/0x4D31/venator/internal/model"
)

func TestSourcePreservesTypes(t *testing.T) {
	source := NewSource(strings.NewReader("{\"count\":2,\"ok\":true,\"nothing\":null}\n"), 10, 1<<20)
	records, err := source.Query(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := records[0]["count"].(interface{ String() string }).String(); got != "2" {
		t.Fatalf("count = %q", got)
	}
	if got := records[0]["ok"]; got != true {
		t.Fatalf("ok = %#v", got)
	}
}

func TestSourceEnforcesRecordLimit(t *testing.T) {
	source := NewSource(strings.NewReader("{}\n{}\n"), 1, 1<<20)
	if _, err := source.Query(context.Background(), nil); err == nil {
		t.Fatal("expected record limit error")
	}
}

func TestSourceEnforcesTotalByteLimit(t *testing.T) {
	source := NewSource(strings.NewReader("{\"value\":\"large\"}\n"), 10, 5)
	if _, err := source.Query(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "more than 5 bytes") {
		t.Fatalf("error = %v", err)
	}
}

func TestSourceRejectsTrailingData(t *testing.T) {
	for _, input := range []string{"{\"a\":1} {\"b\":2}\n", "{\"a\":1} garbage\n"} {
		source := NewSource(strings.NewReader(input), 10, 1<<20)
		if _, err := source.Query(context.Background(), nil); err == nil {
			t.Fatalf("input %q was accepted", input)
		}
	}
}

func TestSourceCancellationUnblocksPipeRead(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	source := NewSource(reader, 10, 1<<20)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := source.Query(ctx, nil)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("cancellation took %v", elapsed)
	}
}

func TestFileSourceReadsFiniteNDJSONSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.ndjson")
	if err := os.WriteFile(path, []byte("{\"event\":\"login\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := NewFileSource(10, 1<<20)
	records, err := source.Query(context.Background(), &config.RuleConfig{Query: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0]["event"] != "login" {
		t.Fatalf("records = %#v", records)
	}
}

func TestFileSourceLimitErrorsNameTheFileConnector(t *testing.T) {
	tests := []struct {
		name          string
		contents      string
		maxRecords    int
		maxTotalBytes int64
	}{
		{name: "records", contents: "{}\n{}\n", maxRecords: 1, maxTotalBytes: 1 << 20},
		{name: "bytes", contents: "{\"value\":\"large\"}\n", maxRecords: 10, maxTotalBytes: 5},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "events.ndjson")
			if err := os.WriteFile(path, []byte(test.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			source := NewFileSource(test.maxRecords, test.maxTotalBytes)
			_, err := source.Query(context.Background(), &config.RuleConfig{Query: path})
			if err == nil || !strings.Contains(err.Error(), "file.ndjson") || strings.Contains(err.Error(), "stdin") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestSourceEnforcesFourMiBRecordLimit(t *testing.T) {
	const framing = "{\"value\":\"\"}"
	input := "{\"value\":\"" + strings.Repeat("x", defaultMaxRecordBytes+1-len(framing)) + "\"}\n"
	source := NewSource(strings.NewReader(input), 10, int64(len(input))+1)
	_, err := source.Query(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "stdin.default NDJSON line 1 exceeds 4194304 bytes") {
		t.Fatalf("error = %v", err)
	}
}

func TestFileSourceRejectsNonRegularInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.pipe")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	source := NewFileSource(10, 1<<20)
	if _, err := source.Query(context.Background(), &config.RuleConfig{Query: path}); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("error = %v", err)
	}
}

func TestSinkWritesFindingsAsNDJSON(t *testing.T) {
	var out bytes.Buffer
	sink := NewSink(&out)
	finding := model.Finding{SchemaVersion: model.FindingSchemaVersion, ID: "abc", DetectedAt: time.Unix(0, 0).UTC()}
	if err := sink.Publish(context.Background(), model.PublishBatch{Findings: []model.Finding{finding}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"schema_version":"venator.finding/v1"`) {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestSinkCancellationUnblocksPipeWrite(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	sink := NewSink(writer)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := sink.Publish(ctx, model.PublishBatch{Findings: []model.Finding{{
		SchemaVersion: model.FindingSchemaVersion,
		Payload:       strings.Repeat("x", 1<<20),
	}}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("cancellation took %v", elapsed)
	}
}
