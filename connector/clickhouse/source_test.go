package clickhouse

import (
	"context"
	"encoding/json"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/0x4D31/venator/internal/config"
	ch "github.com/ClickHouse/clickhouse-go/v2"
	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

func column(name string, scanType reflect.Type) chdriver.ColumnType {
	return fakeColumnType{name: name, scanType: scanType}
}

func TestSourceQueryScansTypedRowsAndDereferencesNullableValues(t *testing.T) {
	note := "investigate"
	rows := &fakeRows{
		columns: []string{"actor", "attempts", "note"},
		types: []chdriver.ColumnType{
			column("actor", reflect.TypeOf("")),
			column("attempts", reflect.TypeOf(uint64(0))),
			fakeColumnType{name: "note", nullable: true, scanType: reflect.TypeOf((*string)(nil))},
		},
		values: [][]any{
			{"alice", uint64(3), &note},
			{"bob", uint64(5), nil},
		},
		index: -1,
	}
	var gotQuery string
	var hadDeadline bool
	conn := &fakeConnection{queryFn: func(ctx context.Context, query string, _ ...any) (rowSet, error) {
		gotQuery = query
		_, hadDeadline = ctx.Deadline()
		return rows, nil
	}}
	cfg := baseConfig()
	cfg.Query.MaxRows = 2
	source, err := NewSource(clientForTest(t, cfg, conn))
	if err != nil {
		t.Fatal(err)
	}

	records, err := source.Query(context.Background(), &config.RuleConfig{Query: " SELECT actor, attempts, note FROM auth "})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if gotQuery != "SELECT actor, attempts, note FROM auth" {
		t.Errorf("query = %q", gotQuery)
	}
	if !hadDeadline {
		t.Error("query context had no deadline")
	}
	if len(records) != 2 {
		t.Fatalf("record count = %d, want 2", len(records))
	}
	if records[0]["actor"] != "alice" || records[0]["attempts"] != uint64(3) || records[0]["note"] != note {
		t.Errorf("record 0 = %#v", records[0])
	}
	if records[1]["note"] != nil {
		t.Errorf("nullable note = %#v, want nil", records[1]["note"])
	}
	if rows.closeCalls != 1 {
		t.Errorf("rows Close calls = %d, want 1", rows.closeCalls)
	}
}

func TestQuerySettingsEnforceServerLimit(t *testing.T) {
	settings := querySettings(42, 1024)
	if settings["max_result_rows"] != uint64(42) {
		t.Errorf("max_result_rows = %#v", settings["max_result_rows"])
	}
	if settings["max_result_bytes"] != uint64(1024) {
		t.Errorf("max_result_bytes = %#v", settings["max_result_bytes"])
	}
	if settings["result_overflow_mode"] != "throw" {
		t.Errorf("result_overflow_mode = %#v", settings["result_overflow_mode"])
	}
	ctx := ch.Context(context.Background(), ch.WithSettings(settings))
	if ctx == nil {
		t.Fatal("clickhouse.Context returned nil")
	}
}

func TestSourceQueryEnforcesIndependentClientRowCap(t *testing.T) {
	rows := &fakeRows{
		columns: []string{"value"},
		types:   []chdriver.ColumnType{column("value", reflect.TypeOf(uint8(0)))},
		values:  [][]any{{uint8(1)}, {uint8(2)}},
		index:   -1,
	}
	conn := &fakeConnection{queryFn: func(context.Context, string, ...any) (rowSet, error) {
		return rows, nil
	}}
	cfg := baseConfig()
	cfg.Query.MaxRows = 1
	source, err := NewSource(clientForTest(t, cfg, conn))
	if err != nil {
		t.Fatal(err)
	}

	_, err = source.Query(context.Background(), &config.RuleConfig{Query: "SELECT number FROM numbers(2)"})
	if err == nil || !strings.Contains(err.Error(), "client row limit 1") {
		t.Fatalf("Query() error = %v, want client row limit", err)
	}
	if rows.closeCalls != 1 {
		t.Errorf("rows Close calls = %d, want 1", rows.closeCalls)
	}
}

func TestSourceQueryRejectsMalformedColumns(t *testing.T) {
	rows := &fakeRows{
		columns: []string{"same", "same"},
		types: []chdriver.ColumnType{
			column("same", reflect.TypeOf("")),
			column("same", reflect.TypeOf("")),
		},
		index: -1,
	}
	conn := &fakeConnection{queryFn: func(context.Context, string, ...any) (rowSet, error) {
		return rows, nil
	}}
	source, err := NewSource(clientForTest(t, baseConfig(), conn))
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Query(context.Background(), &config.RuleConfig{Query: "SELECT 1 AS same, 2 AS same"})
	if err == nil || !strings.Contains(err.Error(), "duplicate column") {
		t.Fatalf("Query() error = %v, want duplicate column", err)
	}
}

func TestSourceQueryHonorsParentCancellation(t *testing.T) {
	conn := &fakeConnection{queryFn: func(ctx context.Context, _ string, _ ...any) (rowSet, error) {
		return nil, ctx.Err()
	}}
	source, err := NewSource(clientForTest(t, baseConfig(), conn))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = source.Query(ctx, &config.RuleConfig{Query: "SELECT 1"})
	if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("Query() error = %v, want canceled", err)
	}
}

func TestScannedValueCopiesBytes(t *testing.T) {
	original := []byte("payload")
	target := &original
	got := scannedValue(target).([]byte)
	original[0] = 'X'
	if string(got) != "payload" {
		t.Fatalf("scannedValue() = %q, want an independent copy", got)
	}
}

func TestScannedValuePreservesNativePointerEncoder(t *testing.T) {
	integer := big.NewInt(123456789)
	target := &integer
	got := scannedValue(target)
	if _, ok := got.(*big.Int); !ok {
		t.Fatalf("scannedValue() type = %T, want *big.Int", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != "123456789" {
		t.Fatalf("encoded scannedValue() = %s", encoded)
	}
}

func TestSourceUsesConfiguredTimeout(t *testing.T) {
	var remaining time.Duration
	conn := &fakeConnection{queryFn: func(ctx context.Context, _ string, _ ...any) (rowSet, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("query has no deadline")
		}
		remaining = time.Until(deadline)
		return &fakeRows{index: -1}, nil
	}}
	cfg := baseConfig()
	cfg.Query.Timeout = config.Duration(3 * time.Second)
	source, err := NewSource(clientForTest(t, cfg, conn))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Query(context.Background(), &config.RuleConfig{Query: "SELECT 1 WHERE 0"}); err != nil {
		t.Fatal(err)
	}
	if remaining <= 0 || remaining > 3*time.Second {
		t.Fatalf("remaining deadline = %v, want (0, 3s]", remaining)
	}
}
