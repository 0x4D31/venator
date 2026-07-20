package exclusion

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestExcluderMatchesNestedTypedAndDottedFields(t *testing.T) {
	excluder := loadExcluder(t, `
- name: known automation account
  when: |
    "actor" in event &&
    event.actor != null &&
    "name" in event.actor &&
    event.actor.name == "backup" &&
    event.attempts >= 3 &&
    event.enabled &&
    event.tags.exists(tag, tag == "automation")
- name: expected process
  when: |
    "process.name" in event && event["process.name"] == "loginwindow"
`)

	tests := []struct {
		name  string
		event map[string]any
		want  bool
	}{
		{
			name: "nested typed values",
			event: map[string]any{
				"actor":    map[string]any{"name": "backup"},
				"attempts": int64(3),
				"enabled":  true,
				"tags":     []any{"service", "automation"},
			},
			want: true,
		},
		{
			name:  "literal dotted key",
			event: map[string]any{"process.name": "loginwindow"},
			want:  true,
		},
		{
			name: "typed mismatch",
			event: map[string]any{
				"actor":    map[string]any{"name": "backup"},
				"attempts": int64(2),
				"enabled":  true,
				"tags":     []any{"automation"},
			},
			want: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := excluder.IsExcluded(context.Background(), test.event)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("IsExcluded() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestExcluderSupportsMissingAndNullGuards(t *testing.T) {
	excluder := loadExcluder(t, `
- name: test actor
  when: |
    "actor" in event && event.actor != null && event.actor == "test"
`)

	for _, test := range []struct {
		name  string
		event map[string]any
		want  bool
	}{
		{name: "missing", event: map[string]any{}, want: false},
		{name: "null", event: map[string]any{"actor": nil}, want: false},
		{name: "different", event: map[string]any{"actor": "alice"}, want: false},
		{name: "match", event: map[string]any{"actor": "test"}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := excluder.IsExcluded(context.Background(), test.event)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("IsExcluded() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestExcluderStopsAfterFirstMatch(t *testing.T) {
	excluder := loadExcluder(t, `
- name: match
  when: "true"
- name: would fail
  when: event.missing == "value"
`)

	excluded, err := excluder.IsExcluded(context.Background(), map[string]any{})
	if err != nil || !excluded {
		t.Fatalf("IsExcluded() = %t, %v", excluded, err)
	}
}

func TestExcluderWrapsEvaluationErrorWithName(t *testing.T) {
	excluder := loadExcluder(t, `
- name: actor is test
  when: event.actor == "test"
`)

	_, err := excluder.IsExcluded(context.Background(), map[string]any{})
	if err == nil || !strings.Contains(err.Error(), `evaluate exclusion "actor is test"`) ||
		!strings.Contains(err.Error(), "evaluate CEL expression") {
		t.Fatalf("IsExcluded() error = %v", err)
	}
}

func TestExcluderHonorsCanceledContext(t *testing.T) {
	excluder := loadExcluder(t, `
- name: canceled evaluation
  when: "true"
`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := excluder.IsExcluded(ctx, map[string]any{})
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), `evaluate exclusion "canceled evaluation"`) {
		t.Fatalf("IsExcluded() error = %v", err)
	}
}

func TestExcluderReportsRuntimeCostLimit(t *testing.T) {
	excluder := loadExcluder(t, `
- name: expensive expression
  when: event.values.all(value, value == "x")
`)
	values := make([]any, 100_001)
	for i := range values {
		values[i] = "x"
	}

	_, err := excluder.IsExcluded(context.Background(), map[string]any{"values": values})
	if err == nil || !strings.Contains(err.Error(), `evaluate exclusion "expensive expression"`) ||
		!strings.Contains(err.Error(), "cost limit") {
		t.Fatalf("IsExcluded() error = %v", err)
	}
}

func TestNewExcluderRejectsInvalidExpressions(t *testing.T) {
	for _, test := range []struct {
		name string
		when string
		want string
	}{
		{name: "blank", when: `" \t"`, want: "when cannot be blank"},
		{name: "syntax", when: "event.actor ==", want: "compile CEL expression"},
		{name: "non-boolean", when: "event.actor", want: "CEL expression must return bool"},
	} {
		t.Run(test.name, func(t *testing.T) {
			content := "- name: broken expression\n  when: " + test.when + "\n"
			_, err := NewExcluder(writeExclusions(t, content))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewExcluder() error = %v, want containing %q", err, test.want)
			}
			if test.name != "blank" && !strings.Contains(err.Error(), `compile exclusion "broken expression"`) {
				t.Fatalf("NewExcluder() error does not identify the exclusion: %v", err)
			}
		})
	}
}

func TestNewExcluderRejectsInvalidEntryShape(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "empty document", content: "", want: "top-level value must be a list"},
		{name: "null document", content: "null\n", want: "top-level value must be a list"},
		{name: "mapping document", content: "{}\n", want: "top-level value must be a list"},
		{name: "scalar entry", content: "- invalid\n", want: "exclusion 1 must be a mapping"},
		{name: "null entry", content: "- null\n", want: "exclusion 1 must be a mapping"},
		{name: "non-string key", content: "- 1: value\n", want: "mapping keys must be strings"},
		{name: "unknown field", content: "- name: example\n  when: \"true\"\n  note: no\n", want: `unknown key "note"`},
		{name: "duplicate name field", content: "- name: one\n  name: two\n  when: \"true\"\n", want: `duplicate key "name"`},
		{name: "duplicate when field", content: "- name: one\n  when: \"true\"\n  when: \"false\"\n", want: `duplicate key "when"`},
		{name: "boolean name", content: "- name: true\n  when: \"true\"\n", want: "name must be a YAML string"},
		{name: "null name", content: "- name: null\n  when: \"true\"\n", want: "name must be a YAML string"},
		{name: "boolean when", content: "- name: example\n  when: true\n", want: "when must be a YAML string"},
		{name: "null when", content: "- name: example\n  when: null\n", want: "when must be a YAML string"},
		{name: "missing name", content: "- when: \"true\"\n", want: "name cannot be blank"},
		{name: "missing when", content: "- name: example\n", want: "when cannot be blank"},
		{name: "blank name", content: "- name: \" \\t\"\n  when: \"true\"\n", want: "name cannot be blank"},
		{name: "padded name", content: "- name: \" example \"\n  when: \"true\"\n", want: "leading or trailing whitespace"},
		{name: "control in name", content: "- name: \"example\\nname\"\n  when: \"true\"\n", want: "control characters"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewExcluder(writeExclusions(t, test.content))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewExcluder() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestNewExcluderRequiresUniqueBoundedNames(t *testing.T) {
	t.Run("duplicate", func(t *testing.T) {
		_, err := NewExcluder(writeExclusions(t, `
- name: expected service account
  when: "true"
- name: expected service account
  when: "false"
`))
		if err == nil || !strings.Contains(err.Error(), `duplicate name "expected service account"`) {
			t.Fatalf("NewExcluder() error = %v", err)
		}
	})

	t.Run("Unicode code point limit", func(t *testing.T) {
		accepted := strings.Repeat("🦊", 128)
		if _, err := NewExcluder(writeExclusions(t, "- name: \""+accepted+"\"\n  when: \"true\"\n")); err != nil {
			t.Fatalf("128-code-point name was rejected: %v", err)
		}

		rejected := strings.Repeat("🦊", 129)
		_, err := NewExcluder(writeExclusions(t, "- name: \""+rejected+"\"\n  when: \"true\"\n"))
		if err == nil || !strings.Contains(err.Error(), "128 Unicode code points") {
			t.Fatalf("NewExcluder() error = %v", err)
		}
	})
}

func TestNewExcluderEnforcesEntryLimit(t *testing.T) {
	var content strings.Builder
	for i := 0; i < 256; i++ {
		content.WriteString("- name: exclusion ")
		content.WriteString(strconv.Itoa(i))
		content.WriteString("\n  when: \"false\"\n")
	}
	if _, err := NewExcluder(writeExclusions(t, content.String())); err != nil {
		t.Fatalf("256 exclusions were rejected: %v", err)
	}
	content.WriteString("- name: too many\n  when: \"false\"\n")
	_, err := NewExcluder(writeExclusions(t, content.String()))
	if err == nil || !strings.Contains(err.Error(), "at most 256 exclusions") {
		t.Fatalf("NewExcluder() error = %v", err)
	}
}

func TestNewExcluderRejectsYAMLControlFeatures(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
		want    string
	}{
		{
			name: "merge key",
			content: `
- &base
  name: base
  when: "true"
- <<: *base
`,
			want: "YAML merge keys are not supported",
		},
		{
			name: "alias mapping key",
			content: `
- name: &field when
  *field: "true"
`,
			want: "aliases are not supported as mapping keys",
		},
		{
			name:    "multiple documents",
			content: "[]\n---\n[]\n",
			want:    "multiple YAML documents are not supported",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewExcluder(writeExclusions(t, test.content))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewExcluder() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestNewExcluderAcceptsEmptyListAndValueAliases(t *testing.T) {
	empty, err := NewExcluder(writeExclusions(t, "[]\n"))
	if err != nil {
		t.Fatal(err)
	}
	excluded, err := empty.IsExcluded(context.Background(), map[string]any{})
	if err != nil || excluded {
		t.Fatalf("empty IsExcluded() = %t, %v", excluded, err)
	}

	aliased := loadExcluder(t, `
- name: &name expected actor
  when: &condition event.actor == "test"
- name: another exclusion
  when: *condition
`)
	excluded, err = aliased.IsExcluded(context.Background(), map[string]any{"actor": "test"})
	if err != nil || !excluded {
		t.Fatalf("aliased IsExcluded() = %t, %v", excluded, err)
	}
}

func TestFixtureExclusionsCompile(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "test-exclusions.yaml")
	if _, err := NewExcluder(path); err != nil {
		t.Fatal(err)
	}
}

func TestNewExcluderRejectsNonRegularAndMissingFiles(t *testing.T) {
	if _, err := NewExcluder(t.TempDir()); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory error = %v", err)
	}
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	if _, err := NewExcluder(missing); err == nil || !strings.Contains(err.Error(), "stat exclusions file") {
		t.Fatalf("missing-file error = %v", err)
	}
}

func loadExcluder(t *testing.T, content string) *Excluder {
	t.Helper()
	excluder, err := NewExcluder(writeExclusions(t, content))
	if err != nil {
		t.Fatal(err)
	}
	return excluder
}

func writeExclusions(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "exclusions.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
