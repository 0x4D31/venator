package clickhouse

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/0x4D31/venator/internal/config"
	ch "github.com/ClickHouse/clickhouse-go/v2"
	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

type fakeConnection struct {
	queryFn        func(context.Context, string, ...any) (rowSet, error)
	prepareBatchFn func(context.Context, string) (writeBatch, error)
	pingErr        error
	closeErr       error
	pingCalls      int
	pingRemaining  time.Duration
	closeCalls     int
}

func (c *fakeConnection) Query(ctx context.Context, query string, args ...any) (rowSet, error) {
	if c.queryFn == nil {
		return nil, errors.New("unexpected Query call")
	}
	return c.queryFn(ctx, query, args...)
}

func (c *fakeConnection) PrepareBatch(ctx context.Context, query string) (writeBatch, error) {
	if c.prepareBatchFn == nil {
		return nil, errors.New("unexpected PrepareBatch call")
	}
	return c.prepareBatchFn(ctx, query)
}

func (c *fakeConnection) Ping(ctx context.Context) error {
	c.pingCalls++
	if deadline, ok := ctx.Deadline(); ok {
		c.pingRemaining = time.Until(deadline)
	}
	return c.pingErr
}

func (c *fakeConnection) Close() error {
	c.closeCalls++
	return c.closeErr
}

type fakeColumnType struct {
	name         string
	nullable     bool
	scanType     reflect.Type
	databaseType string
}

func (c fakeColumnType) Name() string             { return c.name }
func (c fakeColumnType) Nullable() bool           { return c.nullable }
func (c fakeColumnType) ScanType() reflect.Type   { return c.scanType }
func (c fakeColumnType) DatabaseTypeName() string { return c.databaseType }

type fakeRows struct {
	columns    []string
	types      []chdriver.ColumnType
	values     [][]any
	index      int
	scanErr    error
	rowsErr    error
	closeErr   error
	closeCalls int
}

func (r *fakeRows) Next() bool {
	if r.index+1 >= len(r.values) {
		return false
	}
	r.index++
	return true
}

func (r *fakeRows) Scan(dest ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}
	if r.index < 0 || r.index >= len(r.values) {
		return errors.New("scan called without a current row")
	}
	if len(dest) != len(r.values[r.index]) {
		return errors.New("destination count does not match row")
	}
	for i, value := range r.values[r.index] {
		destination := reflect.ValueOf(dest[i])
		if !destination.IsValid() || destination.Kind() != reflect.Pointer || destination.IsNil() {
			return errors.New("scan destination must be a non-nil pointer")
		}
		destination = destination.Elem()
		if value == nil {
			destination.SetZero()
			continue
		}
		source := reflect.ValueOf(value)
		if source.Type().AssignableTo(destination.Type()) {
			destination.Set(source)
			continue
		}
		if source.Type().ConvertibleTo(destination.Type()) {
			destination.Set(source.Convert(destination.Type()))
			continue
		}
		return errors.New("row value is not assignable to scan destination")
	}
	return nil
}

func (r *fakeRows) ColumnTypes() []chdriver.ColumnType { return r.types }
func (r *fakeRows) Columns() []string                  { return r.columns }
func (r *fakeRows) Err() error                         { return r.rowsErr }
func (r *fakeRows) Close() error {
	r.closeCalls++
	return r.closeErr
}

type fakeBatch struct {
	rows       [][]any
	appendErr  error
	sendErr    error
	closeErr   error
	sendCalls  int
	closeCalls int
}

func (b *fakeBatch) Append(values ...any) error {
	if b.appendErr != nil {
		return b.appendErr
	}
	b.rows = append(b.rows, append([]any(nil), values...))
	return nil
}

func (b *fakeBatch) Send() error {
	b.sendCalls++
	return b.sendErr
}

func (b *fakeBatch) Close() error {
	b.closeCalls++
	return b.closeErr
}

func baseConfig() config.ClickHouseConfig {
	return config.ClickHouseConfig{
		Addresses:   []string{"localhost:9000"},
		Protocol:    "native",
		Database:    "default",
		Username:    "default",
		Compression: "lz4",
		Query: &config.ClickHouseQueryConfig{
			Timeout: config.Duration(time.Second),
			MaxRows: 100,
		},
		Sink: &config.ClickHouseSinkConfig{Table: "venator_findings"},
	}
}

func clientForTest(t *testing.T, cfg config.ClickHouseConfig, conn connection) *Client {
	t.Helper()
	normalized, err := normalizeConfig(cfg)
	if err != nil {
		t.Fatalf("normalizeConfig() error = %v", err)
	}
	return &Client{conn: conn, cfg: normalized}
}

func TestOpenWithBuildsOptionsAndPings(t *testing.T) {
	cfg := baseConfig()
	cfg.Addresses = []string{" clickhouse.example:8443 "}
	cfg.Protocol = "http"
	cfg.Compression = "zstd"
	cfg.Password = "secret"
	cfg.TLS.Enabled = true
	cfg.TLS.ServerName = "clickhouse.example"
	originalAddress := cfg.Addresses[0]
	conn := &fakeConnection{}

	var got *ch.Options
	client, err := openWith(context.Background(), cfg, func(options *ch.Options) (connection, error) {
		got = options
		return conn, nil
	})
	if err != nil {
		t.Fatalf("openWith() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if conn.pingCalls != 1 {
		t.Fatalf("Ping calls = %d, want 1", conn.pingCalls)
	}
	if conn.pingRemaining <= 0 || conn.pingRemaining > defaultDialTimeout {
		t.Errorf("Ping deadline remaining = %v, want (0, %v]", conn.pingRemaining, defaultDialTimeout)
	}
	if got.Protocol != ch.HTTP {
		t.Errorf("Protocol = %v, want HTTP", got.Protocol)
	}
	if len(got.ClientInfo.Products) != 1 || got.ClientInfo.Products[0].Name != "venator" || got.ClientInfo.Products[0].Version != "0.2.0" {
		t.Errorf("ClientInfo products = %#v", got.ClientInfo.Products)
	}
	if got.Compression == nil || got.Compression.Method != ch.CompressionZSTD {
		t.Errorf("Compression = %#v, want zstd", got.Compression)
	}
	if len(got.Addr) != 1 || got.Addr[0] != "clickhouse.example:8443" {
		t.Errorf("Addr = %v", got.Addr)
	}
	if got.Auth.Database != "default" || got.Auth.Password != "secret" {
		t.Errorf("Auth = %#v", got.Auth)
	}
	if got.TLS == nil || got.TLS.ServerName != "clickhouse.example" {
		t.Errorf("TLS = %#v", got.TLS)
	}
	if !got.FreeBufOnConnRelease {
		t.Error("FreeBufOnConnRelease = false, want true")
	}
	if cfg.Addresses[0] != originalAddress {
		t.Errorf("openWith mutated caller config address to %q", cfg.Addresses[0])
	}
}

func TestOpenWithClosesConnectionAfterFailedPing(t *testing.T) {
	conn := &fakeConnection{
		pingErr:  errors.New("authentication failed"),
		closeErr: errors.New("close failed"),
	}
	_, err := openWith(context.Background(), baseConfig(), func(*ch.Options) (connection, error) {
		return conn, nil
	})
	if err == nil || !strings.Contains(err.Error(), "authentication failed") || !strings.Contains(err.Error(), "close failed") {
		t.Fatalf("openWith() error = %v, want ping and close errors", err)
	}
	if conn.closeCalls != 1 {
		t.Fatalf("Close calls = %d, want 1", conn.closeCalls)
	}
}

func TestNormalizeConfigRejectsUnsafeOrInconsistentOptions(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config.ClickHouseConfig)
		want   string
	}{
		{name: "URL address", mutate: func(cfg *config.ClickHouseConfig) { cfg.Addresses = []string{"https://localhost:8443"} }, want: "host:port"},
		{name: "missing port", mutate: func(cfg *config.ClickHouseConfig) { cfg.Addresses = []string{"localhost"} }, want: "host:port"},
		{name: "empty host", mutate: func(cfg *config.ClickHouseConfig) { cfg.Addresses = []string{":9000"} }, want: "host:port"},
		{name: "bad protocol", mutate: func(cfg *config.ClickHouseConfig) { cfg.Protocol = "tcp" }, want: "unsupported protocol"},
		{name: "bad compression", mutate: func(cfg *config.ClickHouseConfig) { cfg.Compression = "gzip" }, want: "unsupported compression"},
		{name: "idle exceeds open", mutate: func(cfg *config.ClickHouseConfig) { cfg.MaxOpenConns = 1; cfg.MaxIdleConns = 2 }, want: "cannot exceed"},
		{name: "no role", mutate: func(cfg *config.ClickHouseConfig) { cfg.Query = nil; cfg.Sink = nil }, want: "must be configured"},
		{name: "unsafe table", mutate: func(cfg *config.ClickHouseConfig) { cfg.Sink.Table = "findings; DROP TABLE x" }, want: "unsupported characters"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := baseConfig()
			test.mutate(&cfg)
			_, err := normalizeConfig(cfg)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("normalizeConfig() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestNormalizeConfigAcceptsBracketedIPv6(t *testing.T) {
	cfg := baseConfig()
	cfg.Addresses = []string{"[::1]:9000"}
	if _, err := normalizeConfig(cfg); err != nil {
		t.Fatalf("normalizeConfig() error = %v", err)
	}
}

func TestSharedClientClosesExactlyOnce(t *testing.T) {
	conn := &fakeConnection{}
	client := clientForTest(t, baseConfig(), conn)
	source, err := client.Source()
	if err != nil {
		t.Fatal(err)
	}
	sink, err := client.Sink()
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if conn.closeCalls != 1 {
		t.Fatalf("Close calls = %d, want 1", conn.closeCalls)
	}
	if _, err := client.Source(); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("Source after close error = %v, want closed", err)
	}
}
