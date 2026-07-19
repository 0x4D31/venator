package clickhouse

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0x4D31/venator/internal/config"
)

func TestBuildTLSConfigSecureDefaultsAndPrivateCA(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Venator test CA"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}

	tlsConfig, err := buildTLSConfig(config.ClickHouseTLSConfig{
		Enabled:    true,
		ServerName: "clickhouse.home.arpa",
		CAFile:     caPath,
	})
	if err != nil {
		t.Fatalf("buildTLSConfig() error = %v", err)
	}
	if tlsConfig.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %#x, want TLS 1.2", tlsConfig.MinVersion)
	}
	if tlsConfig.ServerName != "clickhouse.home.arpa" || tlsConfig.InsecureSkipVerify {
		t.Errorf("TLS config = %#v", tlsConfig)
	}
	if tlsConfig.RootCAs == nil {
		t.Fatal("RootCAs is nil")
	}
	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "clickhouse.home.arpa"},
		DNSNames:     []string{"clickhouse.home.arpa"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, template, &leafKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: tlsConfig.RootCAs, DNSName: "clickhouse.home.arpa"}); err != nil {
		t.Fatalf("private CA was not appended to roots: %v", err)
	}
}

func TestBuildTLSConfigRejectsIgnoredOrInvalidMaterial(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.ClickHouseTLSConfig
		want string
	}{
		{name: "options while disabled", cfg: config.ClickHouseTLSConfig{ServerName: "ignored.example"}, want: "enabled is false"},
		{name: "certificate without key", cfg: config.ClickHouseTLSConfig{Enabled: true, CertFile: "client.pem"}, want: "configured together"},
		{name: "missing CA", cfg: config.ClickHouseTLSConfig{Enabled: true, CAFile: "/does/not/exist"}, want: "read TLS CA file"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := buildTLSConfig(test.cfg)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("buildTLSConfig() error = %v, want containing %q", err, test.want)
			}
		})
	}
}
