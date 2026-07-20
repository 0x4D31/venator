package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCommandSupportsLocalNDJSONAndReport(t *testing.T) {
	dir := t.TempDir()
	global := writeTestFile(t, dir, "global.yaml", "runtime:\n  maxRecords: 10\n  timeout: 1m\n")
	rule := writeTestFile(t, dir, "rule.yaml", `name: local
uid: rule-1
status: stable
confidence: high
enabled: true
queryEngine: stdin.default
publishers: [stdout.default]
language: NDJSON
query: ""
output:
  format: raw
  fields: []
`)
	report := filepath.Join(dir, "run.json")
	var stdout, stderr bytes.Buffer
	code := realMain([]string{"run", "--global-config", global, "--rule-config", rule, "--report-file", report}, strings.NewReader("{\"count\":2}\n"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"schema_version":"venator.finding/v1"`) || !strings.Contains(stdout.String(), `"count":2`) {
		t.Fatalf("stdout=%s", stdout.String())
	}
	contents, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(contents, []byte(`"status": "succeeded"`)) {
		t.Fatalf("report=%s", contents)
	}
}

func TestValidateRejectsUnknownConnectorReference(t *testing.T) {
	dir := t.TempDir()
	global := writeTestFile(t, dir, "global.yaml", "runtime:\n  maxRecords: 10\n  timeout: 1m\n")
	rule := writeTestFile(t, dir, "rule.yaml", `name: local
uid: rule-1
status: stable
confidence: high
enabled: true
queryEngine: missing.source
publishers: [stdout.default]
language: SQL
query: SELECT 1
output:
  format: raw
  fields: []
`)
	var stderr bytes.Buffer
	code := realMain([]string{"validate", "--global-config", global, "--rule-config", rule}, strings.NewReader(""), &bytes.Buffer{}, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "not configured") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestLegacyFlagsRemainRunAlias(t *testing.T) {
	dir := t.TempDir()
	global := writeTestFile(t, dir, "global.yaml", "runtime:\n  maxRecords: 10\n  timeout: 1m\n")
	rule := writeTestFile(t, dir, "rule.yaml", `name: disabled
uid: rule-1
status: stable
confidence: low
enabled: false
queryEngine: stdin.default
publishers: [stdout.default]
language: NDJSON
query: ""
output:
  format: raw
  fields: []
`)
	var stderr bytes.Buffer
	code := realMain([]string{"--global-config", global, "--rule-config", rule}, strings.NewReader(""), &bytes.Buffer{}, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunSkipsDisabledRuleWithoutPreflightingDependencies(t *testing.T) {
	dir := t.TempDir()
	global := writeTestFile(t, dir, "global.yaml", "runtime:\n  maxRecords: 10\n  timeout: 1m\n")
	rule := writeTestFile(t, dir, "rule.yaml", `name: disabled
uid: rule-1
status: stable
confidence: low
enabled: false
queryEngine: file.ndjson
publishers: [missing.publisher]
language: NDJSON
query: missing.ndjson
output: {format: raw, fields: []}
llm:
  enabled: true
  prompt: review
`)
	var stderr bytes.Buffer
	code := realMain([]string{"run", "--global-config", global, "--rule-config", rule}, strings.NewReader(""), &bytes.Buffer{}, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestSubcommandHelpReturnsSuccess(t *testing.T) {
	for _, command := range []string{"run", "validate"} {
		t.Run(command, func(t *testing.T) {
			var stderr bytes.Buffer
			if code := realMain([]string{command, "--help"}, strings.NewReader(""), &bytes.Buffer{}, &stderr); code != 0 {
				t.Fatalf("code=%d stderr=%s", code, stderr.String())
			}
			if strings.Contains(stderr.String(), "invalid arguments") {
				t.Fatalf("stderr=%s", stderr.String())
			}
		})
	}
}

func TestInformationalCommandsRejectTrailingArguments(t *testing.T) {
	for _, arguments := range [][]string{
		{"version", "extra"},
		{"help", "extra"},
		{"--version", "extra"},
		{"--help", "extra"},
	} {
		t.Run(strings.Join(arguments, "_"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := realMain(arguments, strings.NewReader(""), &stdout, &stderr); code != 2 {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
			}
			if !strings.Contains(stderr.String(), "unexpected arguments: extra") || stdout.Len() != 0 {
				t.Fatalf("stdout=%s stderr=%s", stdout.String(), stderr.String())
			}
		})
	}
}

func TestUnusedConnectorDoesNotRequireItsSecret(t *testing.T) {
	dir := t.TempDir()
	global := writeTestFile(t, dir, "global.yaml", `runtime:
  maxRecords: 10
  timeout: 1m
slack:
  instances:
    unused:
      webhookURL: ${DEFINITELY_UNSET_SLACK_WEBHOOK}
`)
	rule := writeTestFile(t, dir, "rule.yaml", `name: local
uid: rule-1
status: stable
confidence: low
enabled: true
queryEngine: stdin.default
publishers: [stdout.default]
language: NDJSON
query: ""
output: {format: raw, fields: []}
`)
	var stdout, stderr bytes.Buffer
	code := realMain([]string{"run", "--global-config", global, "--rule-config", rule}, strings.NewReader("{\"event\":\"ok\"}\n"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestRequiredSinkIsPreflightedBeforeZeroFindingQuery(t *testing.T) {
	dir := t.TempDir()
	global := writeTestFile(t, dir, "global.yaml", `runtime:
  maxRecords: 10
  timeout: 1m
slack:
  instances:
    alerts:
      webhookURL: ${DEFINITELY_UNSET_SLACK_WEBHOOK}
`)
	rule := writeTestFile(t, dir, "rule.yaml", `name: local
uid: rule-1
status: stable
confidence: low
enabled: true
queryEngine: stdin.default
publishers: [slack.alerts]
language: NDJSON
query: ""
output: {format: raw, fields: []}
`)
	var stderr bytes.Buffer
	code := realMain([]string{"run", "--global-config", global, "--rule-config", rule}, strings.NewReader(""), &bytes.Buffer{}, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "DEFINITELY_UNSET_SLACK_WEBHOOK") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestMissingBestEffortSinkIsNonTerminalAtRunButInvalidAtValidate(t *testing.T) {
	dir := t.TempDir()
	global := writeTestFile(t, dir, "global.yaml", "runtime:\n  maxRecords: 10\n  timeout: 1m\n")
	rule := writeTestFile(t, dir, "rule.yaml", `name: local
uid: rule-1
status: stable
confidence: low
enabled: true
queryEngine: stdin.default
publishers: [stdout.default]
bestEffortPublishers: [missing.optional]
language: NDJSON
query: ""
output: {format: raw, fields: []}
`)
	var stdout, stderr bytes.Buffer
	code := realMain([]string{"run", "--global-config", global, "--rule-config", rule}, strings.NewReader("{\"event\":\"ok\"}\n"), &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), `"schema_version":"venator.finding/v1"`) ||
		!strings.Contains(stderr.String(), "best-effort publisher failed") {
		t.Fatalf("run code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	stderr.Reset()
	code = realMain([]string{"validate", "--global-config", global, "--rule-config", rule}, strings.NewReader(""), &bytes.Buffer{}, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), `publisher "missing.optional" is not configured`) {
		t.Fatalf("validate code=%d stderr=%s", code, stderr.String())
	}
}

func TestValidateRejectsMissingFileSource(t *testing.T) {
	dir := t.TempDir()
	global := writeTestFile(t, dir, "global.yaml", "runtime:\n  maxRecords: 10\n  timeout: 1m\n")
	rule := writeTestFile(t, dir, "rule.yaml", `name: local
uid: rule-1
status: stable
confidence: low
enabled: true
queryEngine: file.ndjson
publishers: [stdout.default]
language: NDJSON
query: missing.ndjson
output: {format: raw, fields: []}
`)
	var stderr bytes.Buffer
	code := realMain([]string{"validate", "--global-config", global, "--rule-config", rule}, strings.NewReader(""), &bytes.Buffer{}, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "missing.ndjson") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestLLMInitializationFailureDoesNotSuppressFindings(t *testing.T) {
	dir := t.TempDir()
	global := writeTestFile(t, dir, "global.yaml", `runtime:
  maxRecords: 10
  timeout: 1m
llm:
  provider: openai
  model: test-model
`)
	rule := writeTestFile(t, dir, "rule.yaml", `name: advisory
uid: rule-1
status: stable
confidence: high
enabled: true
queryEngine: stdin.default
publishers: [stdout.default]
language: NDJSON
query: ""
output:
  format: raw
  fields: []
llm:
  enabled: true
  prompt: review this finding
`)
	var stdout, stderr bytes.Buffer
	code := realMain([]string{"run", "--global-config", global, "--rule-config", rule}, strings.NewReader("{\"count\":2}\n"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"schema_version":"venator.finding/v1"`) {
		t.Fatalf("finding was not published: %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "LLM review unavailable") {
		t.Fatalf("optional review failure was not logged: %s", stderr.String())
	}
}

func TestRequiredLLMInitializationFailurePublishesThenFails(t *testing.T) {
	dir := t.TempDir()
	global := writeTestFile(t, dir, "global.yaml", `runtime:
  maxRecords: 10
  timeout: 1m
llm:
  provider: openai
  model: test-model
`)
	rule := writeTestFile(t, dir, "rule.yaml", `name: required-review
uid: rule-1
status: stable
confidence: high
enabled: true
queryEngine: stdin.default
publishers: [stdout.default]
language: NDJSON
query: ""
output:
  format: raw
  fields: []
llm:
  enabled: true
  required: true
  prompt: review this finding
`)
	var stdout, stderr bytes.Buffer
	code := realMain([]string{"run", "--global-config", global, "--rule-config", rule}, strings.NewReader("{\"count\":2}\n"), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"schema_version":"venator.finding/v1"`) {
		t.Fatalf("finding was not published: %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "required LLM review failed after deterministic findings were published") {
		t.Fatalf("stderr=%s", stderr.String())
	}
}

func writeTestFile(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
