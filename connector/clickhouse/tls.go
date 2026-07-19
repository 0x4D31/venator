package clickhouse

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"

	"github.com/0x4D31/venator/internal/config"
)

func buildTLSConfig(cfg config.ClickHouseTLSConfig) (*tls.Config, error) {
	if !cfg.Enabled {
		if cfg.ServerName != "" || cfg.CAFile != "" || cfg.CertFile != "" ||
			cfg.KeyFile != "" || cfg.InsecureSkipVerify {
			return nil, errors.New("TLS options are set while tls.enabled is false")
		}
		return nil, nil
	}
	if (cfg.CertFile == "") != (cfg.KeyFile == "") {
		return nil, errors.New("TLS certFile and keyFile must be configured together")
	}

	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         cfg.ServerName,
		InsecureSkipVerify: cfg.InsecureSkipVerify, // #nosec G402 -- explicit self-hosted opt-out.
	}
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read TLS CA file %q: %w", cfg.CAFile, err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if ok := roots.AppendCertsFromPEM(pem); !ok {
			return nil, fmt.Errorf("TLS CA file %q contains no certificates", cfg.CAFile)
		}
		tlsConfig.RootCAs = roots
	}
	if cfg.CertFile != "" {
		certificate, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load TLS client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}
	return tlsConfig, nil
}
