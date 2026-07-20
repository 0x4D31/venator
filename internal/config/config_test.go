package config_test

import (
	"fmt"
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
			name:        "ExistentConfig",
			filePath:    existentConfigPath,
			expectedErr: "",
			wantCfg:     makeRuleConfig(),
		},
		{
			name:        "NonExistentConfig",
			filePath:    nonExistentConfigPath,
			expectedErr: "failed to open config file",
			wantCfg:     nil,
		},
		{
			name:        "InvalidYAML",
			filePath:    invalidConfigPath,
			expectedErr: "failed to decode config YAML",
			wantCfg:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

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
				if diff := cmp.Diff(tt.wantCfg, cfg); diff != "" {
					t.Errorf("ParseConfig() mismatch (-want +got):\n%s", diff)
				}
			}
		})
	}
}

func makeRuleConfig() *config.RuleConfig {
	return &config.RuleConfig{
		Name:           "test-rule",
		UID:            "2001416b-bdd3-4a31-af52-3b1933c4f926",
		Status:         "test",
		Confidence:     config.ConfidenceLow,
		Enabled:        true,
		Schedule:       "0 */2 * * *",
		QueryEngine:    "opensearch.dev",
		ExclusionsPath: "test-exclusions.yaml",
		Publishers:     []string{"opensearch.dev", "stdout.default"},
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
	t.Setenv("OPENSEARCH_DEV_PASSWORD", "dev-secret-password")
	t.Setenv("OPENSEARCH_PROD_PASSWORD", "prod-secret-password")
	t.Setenv("LLM_API_KEY", "test-api-key")

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

func TestParseRuleConfigRejectsInvalidEnabledAndScheduleTypes(t *testing.T) {
	base, err := os.ReadFile(existentConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		old         string
		replacement string
		want        string
	}{
		{name: "null enabled", old: "enabled: true", replacement: "enabled: null", want: "enabled must be a YAML boolean"},
		{name: "null schedule", old: "schedule: \"0 */2 * * *\"", replacement: "schedule: null", want: "schedule must be a YAML string"},
		{name: "numeric schedule", old: "schedule: \"0 */2 * * *\"", replacement: "schedule: 5", want: "schedule must be a YAML string"},
		{name: "boolean schedule", old: "schedule: \"0 */2 * * *\"", replacement: "schedule: true", want: "schedule must be a YAML string"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contents := strings.Replace(string(base), tt.old, tt.replacement, 1)
			if contents == string(base) {
				t.Fatalf("fixture does not contain %q", tt.old)
			}
			path := filepath.Join(t.TempDir(), "rule.yaml")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := config.ParseRuleConfig(path); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestConfigParsersRejectNonRegularFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config-dir")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := config.ParseGlobalConfig(path); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("global error = %v", err)
	}
	if _, err := config.ParseRuleConfig(path); err == nil || !strings.Contains(err.Error(), "not a regular file") {
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

func TestResolveEnvRejectsMalformedReferencesWithoutPartialExpansion(t *testing.T) {
	t.Setenv("TOKEN", "resolved")
	value := struct {
		Valid     string
		Malformed string
	}{Valid: "${TOKEN}", Malformed: "${TOKEN-NAME}"}
	if err := config.ResolveEnv(&value); err == nil || !strings.Contains(err.Error(), "malformed environment reference") {
		t.Fatalf("error = %v", err)
	}
	if value.Valid != "${TOKEN}" {
		t.Fatalf("valid reference was partially expanded: %#v", value)
	}
}

func TestResolveEnvDoesNotInterpretExpansionValues(t *testing.T) {
	t.Setenv("TOKEN", "literal-${NOT_A_REFERENCE}")
	value := struct{ Token string }{Token: "${TOKEN}"}
	if err := config.ResolveEnv(&value); err != nil {
		t.Fatal(err)
	}
	if value.Token != "literal-${NOT_A_REFERENCE}" {
		t.Fatalf("token = %q", value.Token)
	}
}

func TestResolveEnvRequiresPointer(t *testing.T) {
	if err := config.ResolveEnv(struct{ Token string }{Token: "${TOKEN}"}); err == nil || !strings.Contains(err.Error(), "non-nil pointer") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveEnvRejectsBlankCloudIdentifiers(t *testing.T) {
	t.Setenv("BLANK_CLOUD_ID", " \t")
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{
			name:  "PubSub project",
			value: &config.PubSubConfig{ProjectID: "${BLANK_CLOUD_ID}", TopicID: "findings"},
			want:  "projectID and topicID",
		},
		{
			name: "BigQuery dataset",
			value: &config.BigQueryConfig{
				ProjectID: "project", DatasetID: "${BLANK_CLOUD_ID}", TableID: "findings",
			},
			want: "datasetID cannot be blank",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := config.ResolveEnv(test.value); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
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

func TestRuleConfigValidatesBuiltInSourceLanguages(t *testing.T) {
	tests := []struct {
		name        string
		queryEngine string
		language    string
		query       string
		wantError   string
	}{
		{name: "BigQuery SQL", queryEngine: "bigquery.prod", language: "sql", query: "SELECT 1"},
		{name: "ClickHouse SQL", queryEngine: "clickhouse.home", language: "SQL", query: "SELECT 1"},
		{name: "OpenSearch PPL", queryEngine: "opensearch.logs", language: "ppl", query: "source=logs"},
		{name: "OpenSearch rejects EQL", queryEngine: "opensearch.logs", language: "EQL", query: "process where true", wantError: "SQL or PPL"},
		{name: "file NDJSON", queryEngine: "file.ndjson", language: "NDJSON", query: "events.ndjson"},
		{name: "file rejects SQL", queryEngine: "file.ndjson", language: "SQL", query: "events.ndjson", wantError: "requires language NDJSON"},
		{name: "stdin NDJSON", queryEngine: "stdin.default", language: "ndjson"},
		{name: "stdin rejects ignored query", queryEngine: "stdin.default", language: "NDJSON", query: "ignored", wantError: "query must be empty"},
		{name: "custom source language remains extensible", queryEngine: "custom.source", language: "CEL", query: "event.kind == 'alert'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule := *makeRuleConfig()
			rule.QueryEngine = tt.queryEngine
			rule.Language = tt.language
			rule.Query = tt.query
			err := rule.Validate()
			if tt.wantError == "" && err != nil {
				t.Fatal(err)
			}
			if tt.wantError != "" && (err == nil || !strings.Contains(err.Error(), tt.wantError)) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantError)
			}
		})
	}
}

func TestRuleConfigValidatesIdentity(t *testing.T) {
	tests := []struct {
		name     string
		identity *config.Identity
		wantErr  string
	}{
		{name: "omitted"},
		{name: "one field", identity: &config.Identity{Fields: []string{"record_id"}}},
		{name: "literal dotted field", identity: &config.Identity{Fields: []string{"event.id"}}},
		{name: "no fields", identity: &config.Identity{}, wantErr: "identity.fields must contain at least one field"},
		{name: "empty field", identity: &config.Identity{Fields: []string{"record_id", " "}}, wantErr: "identity.fields cannot contain an empty field"},
		{name: "duplicate field", identity: &config.Identity{Fields: []string{"record_id", "record_id"}}, wantErr: `identity.fields contains duplicate field "record_id"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rule := makeRuleConfig()
			rule.Identity = test.identity
			err := rule.Validate()
			if test.wantErr == "" && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("Validate() error = %v, want error containing %q", err, test.wantErr)
			}
		})
	}
}

func TestParseRuleConfigIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rule.yaml")
	contents := `name: local finding
uid: local-finding
confidence: high
enabled: true
queryEngine: stdin.default
language: NDJSON
query: ""
publishers: [stdout.default]
identity:
  fields: [record_id, event.id]
output:
  format: raw
  fields: []
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	rule, err := config.ParseRuleConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"record_id", "event.id"}
	if rule.Identity == nil || !cmp.Equal(rule.Identity.Fields, want) {
		t.Fatalf("identity = %#v, want %#v", rule.Identity, want)
	}
}

func TestParseRuleConfigRejectsNonStringIdentityFields(t *testing.T) {
	for _, value := range []string{"true", "123", "null"} {
		t.Run(value, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rule.yaml")
			contents := fmt.Sprintf(`name: local finding
uid: local-finding
confidence: high
enabled: true
queryEngine: stdin.default
language: NDJSON
query: ""
publishers: [stdout.default]
identity:
  fields: [record_id, %s]
output:
  format: raw
  fields: []
`, value)
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := config.ParseRuleConfig(path); err == nil || !strings.Contains(err.Error(), "identity.fields must contain only YAML strings") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestRuleConfigRequiresRedactionsWithinEvidenceAllowlist(t *testing.T) {
	rule := makeRuleConfig()
	rule.LLM.EvidenceFields = []string{"event", "host"}
	rule.LLM.RedactFields = []string{"token"}
	if err := rule.Validate(); err == nil || !strings.Contains(err.Error(), "must also appear in llm.evidenceFields") {
		t.Fatalf("error = %v", err)
	}

	rule.LLM.RedactFields = []string{"host"}
	if err := rule.Validate(); err != nil {
		t.Fatalf("valid redaction subset rejected: %v", err)
	}
}

func makeGlobalConfig() *config.GlobalConfig {
	return &config.GlobalConfig{
		OpenSearch: config.OpenSearchConnectors{
			Instances: map[string]config.OpenSearchConfig{
				"dev": {
					URL:                "https://opensearch-instance1.example.com:9200",
					Username:           "admin1",
					Password:           "dev-secret-password",
					Index:              "venator-findings-v1",
					SQLFetchSize:       1_000,
					InsecureSkipVerify: false,
				},
				"prod": {
					URL:                "https://opensearch-instance2.example.com:9200",
					Username:           "admin2",
					Password:           "prod-secret-password",
					Index:              "venator-findings-v1",
					InsecureSkipVerify: true,
				},
			},
		},
		LLM: config.LLMConfig{
			Provider:    "openai",
			APIKey:      "test-api-key",
			Model:       "test-model",
			ServerURL:   "",
			Temperature: float64Pointer(0.7),
			Timeout:     config.Duration(30 * time.Second),
		},
		Runtime: config.RuntimeConfig{MaxRecords: 10_000, MaxBytes: 64 << 20, Timeout: config.Duration(15 * time.Minute)},
	}
}

func float64Pointer(value float64) *float64 { return &value }

func TestShippedExamplesParse(t *testing.T) {
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
	if _, err := config.ParseRuleConfig("../../config/examples/llm-review-rule.yaml"); err != nil {
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

func TestGlobalConfigDefersClickHouseStringReferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "global.yaml")
	contents := `clickhouse:
  instances:
    dynamic:
      addresses: ["127.0.0.1:9000"]
      protocol: ${VENATOR_TEST_CLICKHOUSE_PROTOCOL}
      compression: ${VENATOR_TEST_CLICKHOUSE_COMPRESSION}
      sink:
        table: ${VENATOR_TEST_CLICKHOUSE_DATABASE}.${VENATOR_TEST_CLICKHOUSE_TABLE}
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseGlobalConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	instance := cfg.ClickHouse.Instances["dynamic"]
	if instance.Protocol != "${VENATOR_TEST_CLICKHOUSE_PROTOCOL}" {
		t.Fatalf("protocol = %q", instance.Protocol)
	}
	if instance.Compression != "${VENATOR_TEST_CLICKHOUSE_COMPRESSION}" {
		t.Fatalf("compression = %q", instance.Compression)
	}
	if got := instance.Sink.Table; got != "${VENATOR_TEST_CLICKHOUSE_DATABASE}.${VENATOR_TEST_CLICKHOUSE_TABLE}" {
		t.Fatalf("sink.table = %q", got)
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

func TestGlobalConfigRejectsInvalidOpenSearchSQLFetchSize(t *testing.T) {
	tests := []struct {
		name      string
		fetchSize int
		want      string
	}{
		{name: "negative", fetchSize: -1, want: "cannot be negative"},
		{name: "above runtime limit", fetchSize: 10_001, want: "cannot exceed runtime.maxRecords"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := makeGlobalConfig()
			instance := cfg.OpenSearch.Instances["dev"]
			instance.SQLFetchSize = test.fetchSize
			cfg.OpenSearch.Instances["dev"] = instance
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestGlobalConfigRejectsNegativeRuntimeLimits(t *testing.T) {
	tests := []struct {
		name    string
		setting string
		want    string
	}{
		{name: "records", setting: "maxRecords: -1", want: "runtime.maxRecords must be positive"},
		{name: "bytes", setting: "maxBytes: -1", want: "runtime.maxBytes must be positive"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "global.yaml")
			contents := "runtime:\n  " + test.setting + "\nclickhouse:\n  instances:\n    source:\n" +
				"      addresses: [\"localhost:9000\"]\n      query: {}\n"
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := config.ParseGlobalConfig(path); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestGlobalConfigRejectsNonFiniteLLMTemperature(t *testing.T) {
	for _, temperature := range []string{".nan", ".inf", "-.inf"} {
		t.Run(temperature, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "global.yaml")
			contents := "llm:\n  temperature: " + temperature + "\n"
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := config.ParseGlobalConfig(path); err == nil || !strings.Contains(err.Error(), "temperature must be between 0 and 2") {
				t.Fatalf("temperature %s: error = %v", temperature, err)
			}
		})
	}
}

func TestParseGlobalConfigRejectsMalformedEnvironmentReference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "global.yaml")
	if err := os.WriteFile(path, []byte("llm:\n  apiKey: ${API-KEY}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.ParseGlobalConfig(path); err == nil || !strings.Contains(err.Error(), "malformed environment reference") {
		t.Fatalf("error = %v", err)
	}
}

func TestGlobalConfigRejectsInvalidConnectorInstanceNames(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*config.GlobalConfig)
	}{
		{name: "OpenSearch", configure: func(cfg *config.GlobalConfig) {
			cfg.OpenSearch.Instances = map[string]config.OpenSearchConfig{"bad.name": {}}
		}},
		{name: "PubSub", configure: func(cfg *config.GlobalConfig) { cfg.PubSub.Instances = map[string]config.PubSubConfig{"bad.name": {}} }},
		{name: "BigQuery", configure: func(cfg *config.GlobalConfig) {
			cfg.BigQuery.Instances = map[string]config.BigQueryConfig{"bad.name": {}}
		}},
		{name: "Slack", configure: func(cfg *config.GlobalConfig) { cfg.Slack.Instances = map[string]config.SlackConfig{"bad.name": {}} }},
		{name: "ClickHouse", configure: func(cfg *config.GlobalConfig) {
			cfg.ClickHouse.Instances = map[string]config.ClickHouseConfig{"bad.name": {}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.GlobalConfig{
				Runtime: config.RuntimeConfig{MaxRecords: 1, MaxBytes: 1, Timeout: config.Duration(time.Second)},
				LLM:     config.LLMConfig{Timeout: config.Duration(time.Second)},
			}
			tt.configure(&cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "instance name") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestGlobalConfigRejectsBlankCloudIdentifiers(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*config.GlobalConfig)
		want      string
	}{
		{
			name: "PubSub project",
			configure: func(cfg *config.GlobalConfig) {
				cfg.PubSub.Instances = map[string]config.PubSubConfig{"alerts": {ProjectID: " ", TopicID: "findings"}}
			},
			want: "requires projectID and topicID",
		},
		{
			name: "PubSub topic",
			configure: func(cfg *config.GlobalConfig) {
				cfg.PubSub.Instances = map[string]config.PubSubConfig{"alerts": {ProjectID: "project", TopicID: "\t"}}
			},
			want: "requires projectID and topicID",
		},
		{
			name: "BigQuery project",
			configure: func(cfg *config.GlobalConfig) {
				cfg.BigQuery.Instances = map[string]config.BigQueryConfig{"warehouse": {ProjectID: " ", MaxBytesBilled: 1}}
			},
			want: "requires projectID",
		},
		{
			name: "BigQuery dataset",
			configure: func(cfg *config.GlobalConfig) {
				cfg.BigQuery.Instances = map[string]config.BigQueryConfig{
					"warehouse": {ProjectID: "project", DatasetID: " ", TableID: "findings", MaxBytesBilled: 1},
				}
			},
			want: "datasetID cannot be blank",
		},
		{
			name: "BigQuery table",
			configure: func(cfg *config.GlobalConfig) {
				cfg.BigQuery.Instances = map[string]config.BigQueryConfig{
					"warehouse": {ProjectID: "project", DatasetID: "venator", TableID: " ", MaxBytesBilled: 1},
				}
			},
			want: "tableID cannot be blank",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := makeGlobalConfig()
			test.configure(cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}
