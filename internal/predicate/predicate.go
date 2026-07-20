// Package predicate compiles and evaluates bounded CEL predicates over source
// events.
package predicate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
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
	maxEventNesting         = 100
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
	event, err := Prepare(ctx, record)
	if err != nil {
		return false, err
	}
	return p.MatchPrepared(ctx, event)
}

// Prepare converts connector-native values into a JSON-shaped CEL event.
// Values outside CEL's numeric ranges remain lossless strings.
func Prepare(ctx context.Context, record model.Record) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	normalized, err := normalizeMap(ctx, map[string]any(record), 0)
	if err != nil {
		return nil, fmt.Errorf("normalize JSON record: %w", err)
	}
	return normalized, nil
}

// MatchPrepared evaluates an event returned by Prepare. It lets the engine
// share one normalization pass across detection and exclusion predicates.
func (p *Predicate) MatchPrepared(ctx context.Context, event map[string]any) (bool, error) {
	if p == nil {
		return true, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	value, _, err := p.program.ContextEval(ctx, map[string]any{"event": event})
	if err != nil {
		return false, fmt.Errorf("evaluate CEL expression: %w", err)
	}
	matched, ok := value.Value().(bool)
	if !ok {
		return false, fmt.Errorf("CEL expression returned %T, want bool", value.Value())
	}
	return matched, nil
}

func normalizeMap(ctx context.Context, input map[string]any, depth int) (map[string]any, error) {
	if depth > maxEventNesting {
		return nil, fmt.Errorf("event nesting exceeds %d levels", maxEventNesting)
	}
	result := make(map[string]any, len(input))
	for key, rawValue := range input {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		value, err := normalizeValue(ctx, rawValue, depth+1)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", key, err)
		}
		result[key] = value
	}
	return result, nil
}

func normalizeValue(ctx context.Context, value any, depth int) (any, error) {
	if depth > maxEventNesting {
		return nil, fmt.Errorf("event nesting exceeds %d levels", maxEventNesting)
	}
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
		return normalizeMap(ctx, map[string]any(typed), depth)
	case map[string]any:
		return normalizeMap(ctx, typed, depth)
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			normalized, err := normalizeValue(ctx, item, depth+1)
			if err != nil {
				return nil, fmt.Errorf("element %d: %w", i, err)
			}
			result[i] = normalized
		}
		return result, nil
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("unsupported JSON value type %T: %w", value, err)
		}
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.UseNumber()
		var canonical any
		if err := decoder.Decode(&canonical); err != nil {
			return nil, fmt.Errorf("decode JSON representation of %T: %w", value, err)
		}
		return normalizeValue(ctx, canonical, depth)
	}
}

func normalizeNumber(number json.Number) (any, error) {
	text := number.String()
	if strings.ContainsAny(text, ".eE") {
		value, err := strconv.ParseFloat(text, 64)
		if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
			return text, nil
		}
		return value, nil
	}
	if value, err := strconv.ParseInt(text, 10, 64); err == nil {
		return value, nil
	}
	if value, err := strconv.ParseUint(text, 10, 64); err == nil {
		return value, nil
	}
	return text, nil
}

func normalizeFloat(value float64) (float64, error) {
	if math.IsInf(value, 0) || math.IsNaN(value) {
		return 0, fmt.Errorf("non-finite number cannot be represented in JSON")
	}
	return value, nil
}
