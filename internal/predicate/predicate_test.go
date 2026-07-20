package predicate

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/0x4D31/venator/internal/model"
)

func TestPredicateMatchesJSONRecord(t *testing.T) {
	predicate, err := Compile(`
		event.kind == "failed_login" &&
		event.severity >= 4 &&
		has(event.actor) &&
		event.tags.exists(tag, tag == "admin") &&
		event["process.name"] == "loginwindow"
	`)
	if err != nil {
		t.Fatal(err)
	}
	record := model.Record{
		"kind": "failed_login", "severity": json.Number("4.5"), "actor": "alice",
		"tags": []any{"auth", "admin"}, "process.name": "loginwindow",
	}
	matched, err := predicate.Match(context.Background(), record)
	if err != nil || !matched {
		t.Fatalf("Match() = %t, %v", matched, err)
	}
	if _, ok := record["severity"].(json.Number); !ok {
		t.Fatalf("Match mutated record: %#v", record)
	}
}

func TestPredicateCanRejectRecord(t *testing.T) {
	predicate, err := Compile(`has(event.severity) && event.severity >= 5`)
	if err != nil {
		t.Fatal(err)
	}
	matched, err := predicate.Match(context.Background(), model.Record{"event": "login"})
	if err != nil || matched {
		t.Fatalf("Match() = %t, %v", matched, err)
	}
}

func TestCompileRejectsInvalidOrNonBooleanExpression(t *testing.T) {
	for _, test := range []struct {
		name       string
		expression string
		want       string
	}{
		{name: "empty", expression: " \n\t", want: "cannot be empty"},
		{name: "syntax", expression: `event.kind ==`, want: "compile CEL expression"},
		{name: "result type", expression: `event.kind`, want: "must return bool"},
		{name: "size", expression: strings.Repeat("!", maxExpressionCodePoints+1) + "true", want: "compile CEL expression"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Compile(test.expression); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Compile() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestPredicateReportsEvaluationAndInputErrors(t *testing.T) {
	t.Run("missing field", func(t *testing.T) {
		predicate, err := Compile(`event.missing == "value"`)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := predicate.Match(context.Background(), model.Record{}); err == nil || !strings.Contains(err.Error(), "evaluate CEL expression") {
			t.Fatalf("Match() error = %v", err)
		}
	})

	t.Run("unsupported value", func(t *testing.T) {
		predicate, err := Compile(`true`)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := predicate.Match(context.Background(), model.Record{"bad": make(chan int)}); err == nil || !strings.Contains(err.Error(), `field "bad"`) {
			t.Fatalf("Match() error = %v", err)
		}
	})

	t.Run("out of range integer", func(t *testing.T) {
		predicate, err := Compile(`true`)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := predicate.Match(context.Background(), model.Record{"number": json.Number("18446744073709551616")}); err == nil || !strings.Contains(err.Error(), "outside the CEL int and uint ranges") {
			t.Fatalf("Match() error = %v", err)
		}
	})
}

func TestPredicateEnforcesEvaluationCostLimit(t *testing.T) {
	predicate, err := Compile(`event.values.all(value, value == "x")`)
	if err != nil {
		t.Fatal(err)
	}
	values := make([]any, maxEvaluationCost+1)
	for i := range values {
		values[i] = "x"
	}
	if _, err := predicate.Match(context.Background(), model.Record{"values": values}); err == nil || !strings.Contains(err.Error(), "cost limit") {
		t.Fatalf("Match() error = %v", err)
	}
}

func TestPredicateHonorsCanceledContext(t *testing.T) {
	predicate, err := Compile(`true`)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := predicate.Match(ctx, model.Record{"event": "login"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Match() error = %v", err)
	}
}
