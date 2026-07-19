package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0x4D31/venator/internal/config"
	"github.com/google/go-cmp/cmp"
)

const (
	existentConfigPath          = "../../testdata/test-config.yaml"
	existentGlobalConfigPath    = "../../testdata/test-global-config.yaml"
	invalidConfigPath           = "../../testdata/test-invalid-config.yaml"
	invalidGlobalConfigPath     = "../../testdata/test-invalid-global-config.yaml"
	nonExistentConfigPath       = "non_existent_file.yaml"
	nonExistentGlobalConfigPath = "non_existent_global_file.yaml"
)

func TestParseRuleConfig(t *testing.T) {
	tests := []struct {
		name        string
		filePath    string
		expectedErr string
		wantCfg     *config.RuleConfig
	}{
		{
			// Test parsing of an existing, valid config file.
			name:        "ExistentConfig",
			filePath:    existentConfigPath,
			expectedErr: "",
			wantCfg:     MakeRuleConfig(),
		},
		{
			// Test parsing of a non-existent config file.
			name:        "NonExistentConfig",
			filePath:    nonExistentConfigPath,
			expectedErr: "failed to open config file",
			wantCfg:     nil,
		},
		{
			// Test parsing of a config file with invalid YAML (unexpected fields).
			name:        "InvalidYAML",
			filePath:    invalidConfigPath,
			expectedErr: "failed to decode config YAML",
			wantCfg:     nil,
		},
	}

	for _, tt := range tests {
		tt := tt // Capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel() // Enable parallel testing if applicable

			cfg, err := config.ParseRuleConfig(tt.filePath)

			if err != nil {
				if tt.expectedErr == "" {
					t.Fatalf("ParseConfig() unexpected error: %v", err)
				}
				if !strings.Contains(err.Error(), tt.expectedErr) {
					t.Fatalf("ParseConfig() error = %v, expected error containing %q", err, tt.expectedErr)
				}
			} else if tt.expectedErr != "" {
				t.Fatalf("ParseConfig() expected error containing %q, got nil", tt.expectedErr)
			}

			if tt.expectedErr == "" && cfg != nil {
				// Validate parsed fields if parsing was successful.
				if diff := cmp.Diff(tt.wantCfg, cfg); diff != "" {
					t.Errorf("ParseConfig() mismatch (-want +got):\n%s", diff)
				}
			}
		})
	}
}

// MakeRuleConfig constructs the expected Config struct based on testdata/test-config.yaml
func MakeRuleConfig() *config.RuleConfig {
	return &config.RuleConfig{
		Name:           "test-rule",
		UID:            "2001416b-bdd3-4a31-af52-3b1933c4f926",
		Status:         "test",
		Confidence:     config.ConfidenceLow,
		Enabled:        true,
		Schedule:       "0 */2 * * *",
		QueryEngine:    "opensearch",
		ExclusionsPath: "config/exclusions/example-exclusions.yaml",
		Publishers:     []string{"opensearch", "pubsub"},
		Language:       "SQL",
		Query:          "SELECT * FROM logs",
		Output: config.Output{
			Format: config.OutputFormatSignal,
			Fields: []config.OutputField{
				{
					Field:  "Message",
					Source: "f1",
				},
				{
					Field:  "ResourceName",
					Source: "f2",
				},
			},
		},
		Description: "this is a test rule.",
		References:  []string{"https://ref1", "https://ref2"},
		Tags:        []string{"test"},
		Author:      "adelka",
		TTPs: []config.TTP{
			{
				Framework: "MITRE ATT&CK",
				Tactic:    "tactic1",
				Name:      "technique1",
				ID:        "T111",
				Reference: "https://example1.com",
			},
			{
				Framework: "MITRE ATT&CK",
				Tactic:    "tactic2",
				Name:      "technique2",
				ID:        "T222",
				Reference: "https://example2.com",
			},
		},
		LLM: &config.LLM{
			Enabled: true,
			Prompt:  "Prompt template",
		},
	}
}

func TestParseGlobalConfig(t *testing.T) {
	// Set environment variables for testing
	os.Setenv("OPENSEARCH_DEV_PASSWORD", "dev-secret-password")
	os.Setenv("OPENSEARCH_PROD_PASSWORD", "prod-secret-password")
	os.Setenv("LLM_API_KEY", "test-api-key")

	defer func() {
		// Clean up environment variables after the test
		os.Unsetenv("OPENSEARCH_DEV_PASSWORD")
		os.Unsetenv("OPENSEARCH_PROD_PASSWORD")
		os.Unsetenv("LLM_API_KEY")
	}()

	tests := []struct {
		name        string
		filePath    string
		expectedErr string
		wantCfg     *config.GlobalConfig
	}{
		{
			name:        "ExistentGlobalConfig",
			filePath:    existentGlobalConfigPath,
			expectedErr: "",
			wantCfg:     makeGlobalConfig(),
		},
		{
			name:        "NonExistentGlobalConfig",
			filePath:    nonExistentGlobalConfigPath,
			expectedErr: "failed to read global config file",
			wantCfg:     nil,
		},
		{
			name:        "InvalidGlobalYAML",
			filePath:    invalidGlobalConfigPath,
			expectedErr: "failed to decode global config YAML",
			wantCfg:     nil,
		},
	}

	for _, tt := range tests {
		tt := tt // Capture range variable
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := config.ParseGlobalConfig(tt.filePath)
			if err == nil {
				err = config.ResolveEnv(cfg)
			}

			if err != nil {
				if tt.expectedErr == "" {
					t.Fatalf("ParseGlobalConfig() unexpected error: %v", err)
				}
				if !strings.Contains(err.Error(), tt.expectedErr) {
					t.Fatalf("ParseGlobalConfig() error = %v, expected error containing %q", err, tt.expectedErr)
				}
			} else if tt.expectedErr != "" {
				t.Fatalf("ParseGlobalConfig() expected error containing %q, got nil", tt.expectedErr)
			}

			if tt.expectedErr == "" && cfg != nil {
				// Validate parsed fields if parsing was successful.
				if diff := cmp.Diff(tt.wantCfg, cfg); diff != "" {
					t.Errorf("ParseGlobalConfig() mismatch (-want +got):\n%s", diff)
				}
			}
		})
	}
}

func TestConfigParsersRejectMultipleYAMLDocuments(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "global.yaml")
	if err := os.WriteFile(global, []byte("runtime:\n  maxRecords: 10\n---\nruntime:\n  maxRecords: 20\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.ParseGlobalConfig(global); err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("global error = %v", err)
	}
	rule := filepath.Join(dir, "rule.yaml")
	contents := `name: one
uid: one
confidence: low
enabled: false
queryEngine: stdin.default
publishers: [stdout.default]
language: NDJSON
query: ""
output: {format: raw, fields: []}
---
name: two
`
	if err := os.WriteFile(rule, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.ParseRuleConfig(rule); err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("rule error = %v", err)
	}
}

func TestResolveEnvAfterDecodePreservesSecretCharacters(t *testing.T) {
	t.Setenv("SLACK_WEBHOOK", "https://hooks.slack.test/services/a # b\nnot-yaml: true")
	path := filepath.Join(t.TempDir(), "global.yaml")
	if err := os.WriteFile(path, []byte("slack:\n  instances:\n    alerts:\n      webhookURL: ${SLACK_WEBHOOK}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseGlobalConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	instance := cfg.Slack.Instances["alerts"]
	if err := config.ResolveEnv(&instance); err != nil {
		t.Fatal(err)
	}
	if instance.WebhookURL != os.Getenv("SLACK_WEBHOOK") {
		t.Fatalf("webhook = %q", instance.WebhookURL)
	}
}

func TestResolveEnvExpandsOnlyBracedReferences(t *testing.T) {
	t.Setenv("TOKEN", "resolved")
	value := struct {
		Braced string
		Bare   string
		Dollar string
	}{
		Braced: "prefix-${TOKEN}-suffix",
		Bare:   "$TOKEN",
		Dollar: "$2a$literal-secret",
	}
	if err := config.ResolveEnv(&value); err != nil {
		t.Fatal(err)
	}
	if value.Braced != "prefix-resolved-suffix" {
		t.Fatalf("braced reference = %q", value.Braced)
	}
	if value.Bare != "$TOKEN" || value.Dollar != "$2a$literal-secret" {
		t.Fatalf("literal dollar values changed: %#v", value)
	}
}

func TestResolveEnvRetainsMissingReferenceForRetry(t *testing.T) {
	value := struct{ Token string }{Token: "${LATER_TOKEN}"}
	if err := config.ResolveEnv(&value); err == nil || !strings.Contains(err.Error(), "LATER_TOKEN") {
		t.Fatalf("error = %v", err)
	}
	if value.Token != "${LATER_TOKEN}" {
		t.Fatalf("missing reference was destroyed: %q", value.Token)
	}
	t.Setenv("LATER_TOKEN", "available")
	if err := config.ResolveEnv(&value); err != nil {
		t.Fatal(err)
	}
	if value.Token != "available" {
		t.Fatalf("token = %q", value.Token)
	}
}

func TestFileSourcePathResolvesRelativeToRule(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rule.yaml")
	contents := `name: local-file
uid: local-file
confidence: low
enabled: true
queryEngine: file.ndjson
publishers: [stdout.default]
language: NDJSON
query: events.ndjson
output: {format: raw, fields: []}
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	rule, err := config.ParseRuleConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if rule.Query != filepath.Join(dir, "events.ndjson") {
		t.Fatalf("query = %q", rule.Query)
	}
}

func makeGlobalConfig() *config.GlobalConfig {
	return &config.GlobalConfig{
		OpenSearch: config.OpenSearchConnectors{
			Instances: map[string]config.OpenSearchConfig{
				"dev": {
					URL:                "https://opensearch-instance1.example.com:9200",
					Username:           "admin1",
					Password:           "dev-secret-password", // Expect the expanded value from the envVar
					InsecureSkipVerify: false,
				},
				"prod": {
					URL:                "https://opensearch-instance2.example.com:9200",
					Username:           "admin2",
					Password:           "prod-secret-password", // Expect the expanded value from the envVar
					InsecureSkipVerify: true,
				},
			},
		},
		LLM: config.LLMConfig{
			Provider:    "openai",
			APIKey:      "test-api-key", // Expect the expanded value from the envVar
			Model:       "gpt-4o",
			ServerURL:   "",
			Temperature: 0.7,
			Timeout:     config.Duration(30 * time.Second),
		},
		Runtime: config.RuntimeConfig{MaxRecords: 10_000, MaxBytes: 64 << 20, Timeout: config.Duration(15 * time.Minute)},
	}
}

func TestShippedLocalAndClickHouseExamplesParse(t *testing.T) {
	if _, err := config.ParseGlobalConfig("../../config/files/global_config.yaml"); err != nil {
		t.Fatal(err)
	}
	rules, err := filepath.Glob("../../config/rules/*/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) == 0 {
		t.Fatal("no shipped rules found")
	}
	for _, rule := range rules {
		if _, err := config.ParseRuleConfig(rule); err != nil {
			t.Fatalf("%s: %v", rule, err)
		}
	}
	t.Setenv("CLICKHOUSE_PASSWORD", "test-only")
	if _, err := config.ParseGlobalConfig("../../config/examples/clickhouse-global.yaml"); err != nil {
		t.Fatal(err)
	}
	if _, err := config.ParseRuleConfig("../../config/examples/clickhouse-rule.yaml"); err != nil {
		t.Fatal(err)
	}
}

func TestGlobalConfigRejectsUnsafeClickHouseTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "global.yaml")
	contents := `clickhouse:
  instances:
    sink:
      addresses: ["localhost:9000"]
      sink:
        table: "findings; DROP TABLE logs"
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.ParseGlobalConfig(path); err == nil {
		t.Fatal("expected unsafe table error")
	}
}

func TestGlobalConfigRejectsClickHouseRowsAboveRuntimeLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "global.yaml")
	contents := `runtime:
  maxRecords: 10
clickhouse:
  instances:
    source:
      addresses: ["localhost:9000"]
      query:
        maxRows: 11
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.ParseGlobalConfig(path); err == nil || !strings.Contains(err.Error(), "cannot exceed runtime.maxRecords") {
		t.Fatalf("error = %v", err)
	}
}
