package model

import (
	"encoding/json"
	"strings"
	"testing"
)

type textualValue string

func (v textualValue) MarshalText() ([]byte, error) { return []byte(v), nil }

func TestFindingIDIsStableAcrossMapOrder(t *testing.T) {
	first, err := FindingID("rule", Record{"b": 2, "a": "one"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := FindingID("rule", Record{"a": "one", "b": 2})
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(first) != 64 {
		t.Fatalf("IDs = %q, %q", first, second)
	}
	other, err := FindingID("other-rule", Record{"a": "one", "b": 2})
	if err != nil {
		t.Fatal(err)
	}
	if first == other {
		t.Fatal("rule ID was not included")
	}
}

func TestFindingIDKeepsIdenticalOccurrencesDistinctAndStable(t *testing.T) {
	record := Record{"event": "same"}
	first, err := FindingIDForOccurrence("rule", record, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := FindingIDForOccurrence("rule", record, 1)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := FindingIDForOccurrence("rule", record, 1)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || second != retry {
		t.Fatalf("IDs = first %q, second %q, retry %q", first, second, retry)
	}
}

func TestNewRunIDLooksLikeUUIDv4(t *testing.T) {
	id, err := NewRunID()
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(id, "-")
	if len(parts) != 5 || len(id) != 36 || parts[2][0] != '4' {
		t.Fatalf("run ID = %q", id)
	}
}

func TestStringValueUnquotesJSONTextValues(t *testing.T) {
	for _, value := range []any{textualValue("host.example"), typeAlias("actor@example.com")} {
		got, ok := StringValue(value)
		if !ok || strings.Contains(got, `"`) {
			t.Fatalf("StringValue(%T) = %q, %t", value, got, ok)
		}
	}
	got, ok := StringValue(map[string]any{"count": 2})
	if !ok || got != `{"count":2}` {
		t.Fatalf("object StringValue = %q, %t", got, ok)
	}
}

func TestStringValueTreatsTypedNilAsEmpty(t *testing.T) {
	var pointer *string
	var object map[string]any
	var values []int
	for _, value := range []any{pointer, object, values} {
		if got, ok := StringValue(value); !ok || got != "" {
			t.Fatalf("StringValue(%T) = %q, %t", value, got, ok)
		}
	}
}

type typeAlias string

func TestFindingOmitsEmptyAttributes(t *testing.T) {
	encoded, err := json.Marshal(Finding{Payload: Record{"event": "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"attributes"`) {
		t.Fatalf("empty attributes were serialized: %s", encoded)
	}
	finding := Finding{Attributes: FindingAttributes{Message: "detected"}}
	encoded, err = json.Marshal(finding)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"attributes":{"message":"detected"}`) {
		t.Fatalf("populated attributes were omitted: %s", encoded)
	}
}
