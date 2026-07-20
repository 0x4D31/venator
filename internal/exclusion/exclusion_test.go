package exclusion

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0x4D31/venator/internal/model"
)

func TestExcluder(t *testing.T) {
	yamlPath := filepath.Join("..", "..", "testdata", "test-exclusions.yaml")

	excluder, err := NewExcluder(yamlPath)
	if err != nil {
		t.Fatalf("failed to create Excluder: %v", err)
	}

	tests := []struct {
		result   model.Record
		excluded bool
	}{
		{
			result: model.Record{
				"username":   "test",
				"ip_address": "192.168.1.1",
			},
			excluded: true,
		},
		{
			result: model.Record{
				"email":    "user@example.com",
				"domain":   "external.com",
				"username": "user1",
			},
			excluded: true,
		},
		{
			result: model.Record{
				"email":    "user@external.com",
				"domain":   "internal.local",
				"username": "user2",
			},
			excluded: true,
		},
		{
			result: model.Record{
				"response_time": "fast",
				"status_code":   "200",
			},
			excluded: true,
		},
		{
			result: model.Record{
				"user_role": "admin",
				"username":  "adminuser",
			},
			excluded: true,
		},
		{
			result: model.Record{
				"department": "sales",
			},
			excluded: true,
		},
		{
			result: model.Record{
				"status": "inactive",
			},
			excluded: true,
		},
		{
			result: model.Record{
				"region": "eu-west-1",
			},
			excluded: true,
		},
		{
			result: model.Record{
				"user_role": "user",
				"username":  "regularuser",
			},
			excluded: false,
		},
		{
			result: model.Record{
				"username":      "test",
				"ip_address":    "10.0.0.1",
				"response_time": "slow",
			},
			excluded: false,
		},
		{
			result: model.Record{
				"url": "https://www.example.com/path",
			},
			excluded: true,
		},
		{
			result: model.Record{
				"url": "http://example.com/anotherpath",
			},
			excluded: true,
		},
		{
			result: model.Record{
				"url": "https://sub.example.com/path",
			},
			excluded: true,
		},
		{
			result: model.Record{
				"url": "https://www.test.com/path",
			},
			excluded: false,
		},
		{
			result: model.Record{
				"url": "ftp://example.com/resource",
			},
			excluded: false,
		},
	}

	for i, tt := range tests {
		excluded := excluder.IsExcluded(tt.result)
		if excluded != tt.excluded {
			t.Errorf("Test case %d: expected excluded=%v, got %v", i+1, tt.excluded, excluded)
		}
	}
}

func TestNewExcluderRejectsUnknownFieldsAndMixedBooleanGroups(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "unknown operator key",
			content: "- conditions:\n    xor: []\n",
			want:    "unknown key \"xor\"",
		},
		{
			name: "unknown condition key",
			content: "- conditions:\n    and:\n      - field: x\n        operator: equals\n" +
				"        value: y\n        unexpected: true\n",
			want: "unknown key \"unexpected\"",
		},
		{
			name: "duplicate operator key",
			content: "- conditions:\n    and:\n      - field: x\n        operator: equals\n        value: y\n" +
				"    and:\n      - field: x\n        operator: equals\n        value: z\n",
			want: "duplicate key \"and\"",
		},
		{
			name: "duplicate condition key",
			content: "- conditions:\n    and:\n      - field: x\n        field: y\n" +
				"        operator: equals\n        value: z\n",
			want: "duplicate key \"field\"",
		},
		{
			name: "both populated",
			content: "- conditions:\n    and:\n      - field: x\n        operator: equals\n        value: y\n" +
				"    or:\n      - field: x\n        operator: equals\n        value: z\n",
			want: "mutually exclusive",
		},
		{
			name: "empty and plus populated or",
			content: "- conditions:\n    and: []\n    or:\n" +
				"      - field: x\n        operator: equals\n        value: z\n",
			want: "mutually exclusive",
		},
		{
			name: "null and plus populated or",
			content: "- conditions:\n    and: null\n    or:\n" +
				"      - field: x\n        operator: equals\n        value: z\n",
			want: "mutually exclusive",
		},
		{
			name: "populated and plus empty or",
			content: "- conditions:\n    and:\n      - field: x\n        operator: equals\n        value: y\n" +
				"    or: []\n",
			want: "mutually exclusive",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "exclusions.yaml")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewExcluder(path); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestNewExcluderRequiresExactlyOneNonEmptyBooleanGroup(t *testing.T) {
	tests := []struct {
		name       string
		conditions string
		want       string
	}{
		{name: "missing", conditions: "{}", want: "one of and/or is required"},
		{name: "empty and", conditions: "{and: []}", want: "and must be non-empty"},
		{name: "null and", conditions: "{and: null}", want: "and must be non-empty"},
		{name: "empty or", conditions: "{or: []}", want: "or must be non-empty"},
		{name: "null or", conditions: "{or: null}", want: "or must be non-empty"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			content := "- conditions: " + test.conditions + "\n"
			path := filepath.Join(t.TempDir(), "exclusions.yaml")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewExcluder(path); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestNewExcluderRejectsMultipleYAMLDocuments(t *testing.T) {
	content := `
- conditions:
    and:
      - field: user
        operator: equals
        value: alice
---
- conditions:
    or:
      - field: user
        operator: equals
        value: bob
`
	path := filepath.Join(t.TempDir(), "exclusions.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewExcluder(path); err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("error = %v", err)
	}
}

func TestNewExcluderRequiresTopLevelList(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "null", content: "null\n"},
		{name: "empty document", content: "---\n"},
		{name: "mapping", content: "{}\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "exclusions.yaml")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewExcluder(path); err == nil || !strings.Contains(err.Error(), "top-level value must be a list") {
				t.Fatalf("error = %v", err)
			}
		})
	}

	path := filepath.Join(t.TempDir(), "exclusions.yaml")
	if err := os.WriteFile(path, []byte("[]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewExcluder(path); err != nil {
		t.Fatalf("explicit empty list should be valid: %v", err)
	}
}

func TestNewExcluderValidatesOperatorShape(t *testing.T) {
	tests := []struct {
		name       string
		condition  string
		wantError  string
		wantRecord model.Record
	}{
		{
			name:      "missing field",
			condition: "operator: equals\n        value: alice",
			wantError: "field is required",
		},
		{
			name:      "missing scalar value",
			condition: "field: user\n        operator: equals",
			wantError: "requires 'value'",
		},
		{
			name:      "empty contains value",
			condition: "field: user\n        operator: contains\n        value: \"\"",
			wantError: "requires a non-empty 'value'",
		},
		{
			name:      "empty regex value",
			condition: "field: user\n        operator: regex\n        value: \"\"",
			wantError: "requires a non-empty 'value'",
		},
		{
			name:      "scalar operator with values",
			condition: "field: user\n        operator: equals\n        value: alice\n        values: []",
			wantError: "does not accept 'values'",
		},
		{
			name:      "set operator with value",
			condition: "field: user\n        operator: in\n        value: alice\n        values: [alice]",
			wantError: "does not accept 'value'",
		},
		{
			name:      "set operator with omitted values",
			condition: "field: user\n        operator: not_in",
			wantError: "requires non-empty 'values'",
		},
		{
			name:      "set operator with empty values",
			condition: "field: user\n        operator: in\n        values: []",
			wantError: "requires non-empty 'values'",
		},
		{
			name:       "explicit empty equals value",
			condition:  "field: user\n        operator: equals\n        value: \"\"",
			wantRecord: model.Record{"user": ""},
		},
		{
			name:       "explicit empty not-equals value",
			condition:  "field: user\n        operator: not_equals\n        value: \"\"",
			wantRecord: model.Record{"user": "alice"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := "- conditions:\n    and:\n      - " + tt.condition + "\n"
			path := filepath.Join(t.TempDir(), "exclusions.yaml")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			excluder, err := NewExcluder(path)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("error = %v, want substring %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !excluder.IsExcluded(tt.wantRecord) {
				t.Fatalf("condition did not exclude %#v", tt.wantRecord)
			}
		})
	}
}
