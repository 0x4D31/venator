// Package predicate compiles and evaluates bounded CEL detection expressions
// for local NDJSON events.
package predicate

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/0x4D31/venator/internal/model"
	"github.com/google/cel-go/cel"
)

const (
	maxExpressionCodePoints = 4_096
	maxParseRecursionDepth  = 100
	maxParseErrorRecovery   = 10
	maxEvaluationCost       = 100_000
	interruptCheckFrequency = 100
)

// Predicate is a compiled, reusable CEL boolean expression.
type Predicate struct {
	program cel.Program
}

// Compile checks expression syntax and type before constructing a bounded
// program.
func Compile(expression string) (*Predicate, error) {
	if strings.TrimSpace(expression) == "" {
		return nil, fmt.Errorf("CEL expression cannot be empty")
	}
	env, err := cel.NewEnv(
		cel.Variable("event", cel.MapType(cel.StringType, cel.DynType)),
		cel.CrossTypeNumericComparisons(true),
		cel.ParserExpressionSizeLimit(maxExpressionCodePoints),
		cel.ParserRecursionLimit(maxParseRecursionDepth),
		cel.ParserErrorRecoveryLimit(maxParseErrorRecovery),
	)
	if err != nil {
		return nil, fmt.Errorf("create CEL environment: %w", err)
	}
	ast, issues := env.Compile(expression)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("compile CEL expression: %w", issues.Err())
	}
	if !ast.OutputType().IsExactType(cel.BoolType) {
		return nil, fmt.Errorf("CEL expression must return bool, got %s", ast.OutputType())
	}
	program, err := env.Program(ast,
		cel.CostLimit(maxEvaluationCost),
		cel.InterruptCheckFrequency(interruptCheckFrequency),
	)
	if err != nil {
		return nil, fmt.Errorf("create CEL program: %w", err)
	}
	return &Predicate{program: program}, nil
}

// Match evaluates the predicate without mutating record. A nil predicate
// matches every record.
func (p *Predicate) Match(ctx context.Context, record model.Record) (bool, error) {
	if p == nil {
		return true, nil
	}
	normalized, err := normalizeMap(ctx, map[string]any(record))
	if err != nil {
		return false, fmt.Errorf("normalize JSON record: %w", err)
	}
	value, _, err := p.program.ContextEval(ctx, map[string]any{"event": normalized})
	if err != nil {
		return false, fmt.Errorf("evaluate CEL expression: %w", err)
	}
	matched, ok := value.Value().(bool)
	if !ok {
		return false, fmt.Errorf("CEL expression returned %T, want bool", value.Value())
	}
	return matched, nil
}

func normalizeMap(ctx context.Context, input map[string]any) (map[string]any, error) {
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	result := make(map[string]any, len(input))
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		value, err := normalizeValue(ctx, input[key])
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", key, err)
		}
		result[key] = value
	}
	return result, nil
}

func normalizeValue(ctx context.Context, value any) (any, error) {
	switch typed := value.(type) {
	case nil, bool, string:
		return typed, nil
	case json.Number:
		return normalizeNumber(typed)
	case int:
		return int64(typed), nil
	case int8:
		return int64(typed), nil
	case int16:
		return int64(typed), nil
	case int32:
		return int64(typed), nil
	case int64:
		return typed, nil
	case uint:
		return uint64(typed), nil
	case uint8:
		return uint64(typed), nil
	case uint16:
		return uint64(typed), nil
	case uint32:
		return uint64(typed), nil
	case uint64:
		return typed, nil
	case float32:
		return normalizeFloat(float64(typed))
	case float64:
		return normalizeFloat(typed)
	case model.Record:
		return normalizeMap(ctx, map[string]any(typed))
	case map[string]any:
		return normalizeMap(ctx, typed)
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			normalized, err := normalizeValue(ctx, item)
			if err != nil {
				return nil, fmt.Errorf("element %d: %w", i, err)
			}
			result[i] = normalized
		}
		return result, nil
	default:
		return nil, fmt.Errorf("unsupported JSON value type %T", value)
	}
}

func normalizeNumber(number json.Number) (any, error) {
	text := number.String()
	if strings.ContainsAny(text, ".eE") {
		value, err := strconv.ParseFloat(text, 64)
		if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
			return nil, fmt.Errorf("number %q cannot be represented as a CEL double", text)
		}
		return value, nil
	}
	if value, err := strconv.ParseInt(text, 10, 64); err == nil {
		return value, nil
	}
	if value, err := strconv.ParseUint(text, 10, 64); err == nil {
		return value, nil
	}
	return nil, fmt.Errorf("integer %q is outside the CEL int and uint ranges", text)
}

func normalizeFloat(value float64) (float64, error) {
	if math.IsInf(value, 0) || math.IsNaN(value) {
		return 0, fmt.Errorf("non-finite number cannot be represented in JSON")
	}
	return value, nil
}
