// Package clickhouse implements ClickHouse query and finding sink connectors.
package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/0x4D31/venator/internal/config"
	ch "github.com/ClickHouse/clickhouse-go/v2"
	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

const (
	clientProductName      = "venator"
	clientProductVersion   = "0.2.0"
	defaultDialTimeout     = 5 * time.Second
	defaultReadTimeout     = 2 * time.Minute
	defaultConnMaxLifetime = 30 * time.Minute
	defaultMaxOpenConns    = 4
	defaultMaxIdleConns    = 2
	defaultQueryTimeout    = 2 * time.Minute
	defaultMaxRows         = uint64(10_000)
)

// connection deliberately exposes only the driver operations Venator uses.
// The adapter keeps Source and Sink unit tests independent of a live server.
type connection interface {
	Query(ctx context.Context, query string, args ...any) (rowSet, error)
	PrepareBatch(ctx context.Context, query string) (writeBatch, error)
	Ping(ctx context.Context) error
	Close() error
}

type rowSet interface {
	Next() bool
	Scan(dest ...any) error
	ColumnTypes() []chdriver.ColumnType
	Columns() []string
	Close() error
	Err() error
}

type writeBatch interface {
	Append(v ...any) error
	Send() error
	Close() error
}

type driverConnection struct {
	conn chdriver.Conn
}

func (c *driverConnection) Query(ctx context.Context, query string, args ...any) (rowSet, error) {
	return c.conn.Query(ctx, query, args...)
}

func (c *driverConnection) PrepareBatch(ctx context.Context, query string) (writeBatch, error) {
	return c.conn.PrepareBatch(ctx, query)
}

func (c *driverConnection) Ping(ctx context.Context) error { return c.conn.Ping(ctx) }
func (c *driverConnection) Close() error                   { return c.conn.Close() }

type driverOpener func(options *ch.Options) (connection, error)

// Client owns a single concurrency-safe clickhouse-go connection pool. Source
// and Sink are lightweight role-specific wrappers over this shared lifecycle.
type Client struct {
	conn connection
	cfg  config.ClickHouseConfig

	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

// Open creates and verifies a ClickHouse client. clickhouse-go opens lazily, so
// Ping is intentional: bad credentials and unreachable addresses fail during
// registry initialization rather than during a scheduled detection.
func Open(ctx context.Context, cfg config.ClickHouseConfig) (*Client, error) {
	return openWith(ctx, cfg, func(options *ch.Options) (connection, error) {
		conn, err := ch.Open(options)
		if err != nil {
			return nil, err
		}
		return &driverConnection{conn: conn}, nil
	})
}

// ValidateConfig performs the same normalization and local TLS/file checks as
// Open without dialing ClickHouse. The CLI uses it to preflight selected
// connectors before executing a potentially expensive query.
func ValidateConfig(cfg config.ClickHouseConfig) error {
	normalized, err := normalizeConfig(cfg)
	if err != nil {
		return fmt.Errorf("validate ClickHouse config: %w", err)
	}
	if _, err := optionsFromConfig(normalized); err != nil {
		return fmt.Errorf("validate ClickHouse config: %w", err)
	}
	return nil
}

func openWith(ctx context.Context, cfg config.ClickHouseConfig, opener driverOpener) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("open ClickHouse client: %w", err)
	}
	if opener == nil {
		return nil, errors.New("open ClickHouse client: nil driver opener")
	}

	normalized, err := normalizeConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("open ClickHouse client: %w", err)
	}
	options, err := optionsFromConfig(normalized)
	if err != nil {
		return nil, fmt.Errorf("open ClickHouse client: %w", err)
	}
	conn, err := opener(options)
	if err != nil {
		return nil, fmt.Errorf("open ClickHouse driver: %w", err)
	}
	if conn == nil {
		return nil, errors.New("open ClickHouse driver: returned a nil connection")
	}

	client := &Client{conn: conn, cfg: normalized}
	pingCtx, cancel := context.WithTimeout(ctx, normalized.DialTimeout.Value())
	defer cancel()
	if err := conn.Ping(pingCtx); err != nil {
		closeErr := conn.Close()
		return nil, errors.Join(
			fmt.Errorf("ping ClickHouse: %w", err),
			wrapError("close ClickHouse after failed ping", closeErr),
		)
	}
	return client, nil
}

func normalizeConfig(cfg config.ClickHouseConfig) (config.ClickHouseConfig, error) {
	// The config is passed by value, but its slices and role pointers are not.
	// Clone them before normalization so validation and concurrent registry
	// preflight cannot mutate the caller's configuration.
	cfg.Addresses = append([]string(nil), cfg.Addresses...)
	if cfg.Query != nil {
		query := *cfg.Query
		cfg.Query = &query
	}
	if cfg.Sink != nil {
		sink := *cfg.Sink
		cfg.Sink = &sink
	}
	if len(cfg.Addresses) == 0 {
		return cfg, errors.New("at least one address is required")
	}
	for i, address := range cfg.Addresses {
		address = strings.TrimSpace(address)
		if address == "" {
			return cfg, fmt.Errorf("address %d is empty", i)
		}
		if strings.Contains(address, "://") {
			return cfg, fmt.Errorf("address %d must be host:port, not a URL", i)
		}
		host, port, err := net.SplitHostPort(address)
		if err != nil || strings.TrimSpace(host) == "" || strings.TrimSpace(port) == "" || strings.Contains(host, "@") {
			return cfg, fmt.Errorf("address %d must be host:port (IPv6 addresses must be bracketed)", i)
		}
		portNumber, err := strconv.ParseUint(port, 10, 16)
		if err != nil || portNumber == 0 {
			return cfg, fmt.Errorf("address %d must use a numeric port between 1 and 65535", i)
		}
		cfg.Addresses[i] = address
	}

	if cfg.Protocol == "" {
		cfg.Protocol = "native"
	}
	if cfg.Protocol != "native" && cfg.Protocol != "http" {
		return cfg, fmt.Errorf("unsupported protocol %q", cfg.Protocol)
	}
	if cfg.Compression == "" {
		cfg.Compression = "lz4"
	}
	if cfg.Compression != "none" && cfg.Compression != "lz4" && cfg.Compression != "zstd" {
		return cfg, fmt.Errorf("unsupported compression %q", cfg.Compression)
	}

	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = config.Duration(defaultDialTimeout)
	}
	if cfg.ReadTimeout == 0 {
		cfg.ReadTimeout = config.Duration(defaultReadTimeout)
	}
	if cfg.ConnMaxLifetime == 0 {
		cfg.ConnMaxLifetime = config.Duration(defaultConnMaxLifetime)
	}
	if cfg.MaxOpenConns == 0 {
		cfg.MaxOpenConns = defaultMaxOpenConns
	}
	if cfg.MaxIdleConns == 0 {
		cfg.MaxIdleConns = min(defaultMaxIdleConns, cfg.MaxOpenConns)
	}
	if cfg.DialTimeout.Value() <= 0 {
		return cfg, errors.New("dialTimeout must be positive")
	}
	if cfg.ReadTimeout.Value() <= 0 {
		return cfg, errors.New("readTimeout must be positive")
	}
	if cfg.ConnMaxLifetime.Value() <= 0 {
		return cfg, errors.New("connMaxLifetime must be positive")
	}
	if cfg.MaxOpenConns < 1 {
		return cfg, errors.New("maxOpenConns must be positive")
	}
	if cfg.MaxIdleConns < 1 {
		return cfg, errors.New("maxIdleConns must be positive")
	}
	if cfg.MaxIdleConns > cfg.MaxOpenConns {
		return cfg, errors.New("maxIdleConns cannot exceed maxOpenConns")
	}

	if cfg.Query == nil && cfg.Sink == nil {
		return cfg, errors.New("query, sink, or both must be configured")
	}
	if cfg.Query != nil {
		if cfg.Query.Timeout == 0 {
			cfg.Query.Timeout = config.Duration(defaultQueryTimeout)
		}
		if cfg.Query.Timeout.Value() <= 0 {
			return cfg, errors.New("query.timeout must be positive")
		}
		if cfg.Query.MaxRows == 0 {
			cfg.Query.MaxRows = defaultMaxRows
		}
	}
	if cfg.Sink != nil {
		if _, err := quoteTableIdentifier(cfg.Sink.Table); err != nil {
			return cfg, fmt.Errorf("invalid sink table: %w", err)
		}
	}
	return cfg, nil
}

func optionsFromConfig(cfg config.ClickHouseConfig) (*ch.Options, error) {
	tlsConfig, err := buildTLSConfig(cfg.TLS)
	if err != nil {
		return nil, err
	}

	protocol := ch.Native
	if cfg.Protocol == "http" {
		protocol = ch.HTTP
	}
	compression := &ch.Compression{Method: ch.CompressionNone}
	switch cfg.Compression {
	case "lz4":
		compression.Method = ch.CompressionLZ4
	case "zstd":
		compression.Method = ch.CompressionZSTD
	}

	return &ch.Options{
		Protocol: protocol,
		ClientInfo: ch.ClientInfo{
			Products: []struct {
				Name    string
				Version string
			}{
				{Name: clientProductName, Version: clientProductVersion},
			},
		},
		Addr: append([]string(nil), cfg.Addresses...),
		Auth: ch.Auth{
			Database: cfg.Database,
			Username: cfg.Username,
			Password: cfg.Password,
		},
		TLS:                  tlsConfig,
		Compression:          compression,
		DialTimeout:          cfg.DialTimeout.Value(),
		ReadTimeout:          cfg.ReadTimeout.Value(),
		MaxOpenConns:         cfg.MaxOpenConns,
		MaxIdleConns:         cfg.MaxIdleConns,
		ConnMaxLifetime:      cfg.ConnMaxLifetime.Value(),
		FreeBufOnConnRelease: true,
	}, nil
}

// Source returns the query role when this instance has a query section.
func (c *Client) Source() (*Source, error) { return NewSource(c) }

// Sink returns the publisher role when this instance has a sink section.
func (c *Client) Sink() (*Sink, error) { return NewSink(c) }

func (c *Client) ensureOpen() error {
	if c == nil || c.conn == nil {
		return errors.New("ClickHouse client is not initialized")
	}
	if c.closed.Load() {
		return errors.New("ClickHouse client is closed")
	}
	return nil
}

// Close releases the shared connection pool. It is safe for both wrappers to
// call Close; the underlying driver is closed exactly once.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		if c.conn != nil {
			c.closeErr = c.conn.Close()
		}
	})
	return c.closeErr
}

func wrapError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
