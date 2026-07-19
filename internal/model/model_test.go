package model

import (
	"strings"
	"testing"
)

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
