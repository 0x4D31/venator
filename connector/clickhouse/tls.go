package clickhouse

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
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
		pem, err := readTLSFile("CA", cfg.CAFile)
		if err != nil {
			return nil, err
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
		certificatePEM, err := readTLSFile("certificate", cfg.CertFile)
		if err != nil {
			return nil, err
		}
		keyPEM, err := readTLSFile("key", cfg.KeyFile)
		if err != nil {
			return nil, err
		}
		certificate, err := tls.X509KeyPair(certificatePEM, keyPEM)
		if err != nil {
			return nil, fmt.Errorf("load TLS client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}
	return tlsConfig, nil
}

func readTLSFile(label, path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("read TLS %s file %q: %w", label, path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("TLS %s file %q must be a regular file", label, path)
	}
	file, err := os.Open(path) // #nosec G304 -- the operator explicitly configures TLS material paths.
	if err != nil {
		return nil, fmt.Errorf("read TLS %s file %q: %w", label, path, err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect TLS %s file %q: %w", label, path, err)
	}
	if !openedInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("TLS %s file %q changed and is no longer a regular file", label, path)
	}
	contents, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("read TLS %s file %q: %w", label, path, err)
	}
	return contents, nil
}
