package exclusion

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0x4D31/venator/internal/model"
)

func TestExcluder(t *testing.T) {
	// Path to the test exclusion YAML file
	yamlPath := filepath.Join("..", "..", "testdata", "test-exclusions.yaml")

	excluder, err := NewExcluder(yamlPath)
	if err != nil {
		t.Fatalf("failed to create Excluder: %v", err)
	}

	tests := []struct {
		result   model.Record
		excluded bool
	}{
		// Test 'equals' operator with 'and' conditions
		{
			result: model.Record{
				"username":   "test",
				"ip_address": "192.168.1.1",
			},
			excluded: true,
		},
		// Test 'contains' operator with 'or' conditions
		{
			result: model.Record{
				"email":    "user@example.com",
				"domain":   "external.com",
				"username": "user1",
			},
			excluded: true,
		},
		// Test 'equals' operator with 'or' conditions
		{
			result: model.Record{
				"email":    "user@external.com",
				"domain":   "internal.local",
				"username": "user2",
			},
			excluded: true,
		},
		// Test 'equals' operator with 'and' conditions
		{
			result: model.Record{
				"response_time": "fast",
				"status_code":   "200",
			},
			excluded: true,
		},
		// Test 'equals' operator with 'or' conditions
		{
			result: model.Record{
				"user_role": "admin",
				"username":  "adminuser",
			},
			excluded: true,
		},
		// Test 'in' operator
		{
			result: model.Record{
				"department": "sales",
			},
			excluded: true,
		},
		// Test 'not_equals' operator
		{
			result: model.Record{
				"status": "inactive",
			},
			excluded: true,
		},
		// Test 'not_in' operator
		{
			result: model.Record{
				"region": "eu-west-1",
			},
			excluded: true,
		},
		// Test non-excluded result
		{
			result: model.Record{
				"user_role": "user",
				"username":  "regularuser",
			},
			excluded: false,
		},
		// Test partial match for 'and' conditions (should not exclude)
		{
			result: model.Record{
				"username":      "test",
				"ip_address":    "10.0.0.1",
				"response_time": "slow",
			},
			excluded: false,
		},
		// Test 'regex' operator - matching URLs
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
			excluded: true, // Now should pass with updated regex
		},
		// Test 'regex' operator with non-matching URL
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
	tests := []string{
		"- conditions:\n    xor: []\n",
		"- conditions:\n    and:\n      - field: x\n        operator: equals\n        value: y\n    or:\n      - field: x\n        operator: equals\n        value: z\n",
	}
	for _, content := range tests {
		path := filepath.Join(t.TempDir(), "exclusions.yaml")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewExcluder(path); err == nil {
			t.Fatalf("expected error for:\n%s", content)
		}
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
