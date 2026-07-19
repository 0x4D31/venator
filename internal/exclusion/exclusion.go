package exclusion

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/0x4D31/venator/internal/model"
	"gopkg.in/yaml.v3"
)

// Condition represents a single condition in an exclusion rule.
type Condition struct {
	Field    string   `yaml:"field"`
	Operator string   `yaml:"operator"`
	Value    string   `yaml:"value"`
	Values   []string `yaml:"values,omitempty"` // For 'in' and 'not_in' operators
	regex    *regexp.Regexp
}

// ExclusionRule represents a single exclusion rule with conditions.
type ExclusionRule struct {
	Conditions ConditionGroup `yaml:"conditions"`
}

// ConditionGroup defines logical operators for grouping conditions.
type ConditionGroup struct {
	And []Condition `yaml:"and,omitempty"`
	Or  []Condition `yaml:"or,omitempty"`
}

// Excluder manages exclusion rules loaded from a YAML file.
type Excluder struct {
	rules []ExclusionRule
}

// NewExcluder initializes an Excluder by loading rules from a YAML file.
func NewExcluder(path string) (*Excluder, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open exclusions file: %w", err)
	}
	defer file.Close()

	var rules []ExclusionRule
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	if err := decoder.Decode(&rules); err != nil {
		return nil, fmt.Errorf("failed to decode exclusions YAML: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("failed to decode exclusions YAML: multiple YAML documents are not supported")
		}
		return nil, fmt.Errorf("failed to decode exclusions YAML: %w", err)
	}

	// Validate operators and precompile regex patterns.
	for i := range rules {
		if len(rules[i].Conditions.And) > 0 && len(rules[i].Conditions.Or) > 0 {
			return nil, fmt.Errorf("invalid condition group in rule %d: and/or are mutually exclusive", i+1)
		}
		if len(rules[i].Conditions.And) == 0 && len(rules[i].Conditions.Or) == 0 {
			return nil, fmt.Errorf("invalid condition group in rule %d: one of and/or is required", i+1)
		}
		for j := range rules[i].Conditions.And {
			if err := validateCondition(&rules[i].Conditions.And[j]); err != nil {
				return nil, fmt.Errorf("invalid condition in rule %d: %w", i+1, err)
			}
		}
		for j := range rules[i].Conditions.Or {
			if err := validateCondition(&rules[i].Conditions.Or[j]); err != nil {
				return nil, fmt.Errorf("invalid condition in rule %d: %w", i+1, err)
			}
		}
	}

	return &Excluder{rules: rules}, nil
}

// validateCondition checks if the condition has a supported operator and valid regex if needed.
func validateCondition(cond *Condition) error {
	switch cond.Operator {
	case "equals", "contains", "not_equals":
		// No additional validation needed
	case "regex":
		compiled, err := regexp.Compile(cond.Value)
		if err != nil {
			return fmt.Errorf("invalid regex pattern '%s': %w", cond.Value, err)
		}
		cond.regex = compiled
	case "in", "not_in":
		if len(cond.Values) == 0 {
			return fmt.Errorf("operator '%s' requires 'values' field to be non-empty", cond.Operator)
		}
	default:
		return fmt.Errorf("unsupported operator '%s'", cond.Operator)
	}
	return nil
}

// IsExcluded checks if a given result matches any exclusion rule.
// Returns true if excluded, otherwise false.
func (e *Excluder) IsExcluded(result model.Record) bool {
	for _, rule := range e.rules {
		if evaluateConditionGroup(rule.Conditions, result) {
			return true
		}
	}
	return false
}

// evaluateConditionGroup evaluates a group of conditions ("And" or "Or") against the result.
func evaluateConditionGroup(group ConditionGroup, result model.Record) bool {
	if len(group.And) > 0 {
		for _, cond := range group.And {
			if !evaluateCondition(cond, result) {
				return false
			}
		}
		return true
	}

	if len(group.Or) > 0 {
		for _, cond := range group.Or {
			if evaluateCondition(cond, result) {
				return true
			}
		}
		return false
	}

	// No conditions defined; default to not excluded
	return false
}

// evaluateCondition evaluates a single condition against the result.
func evaluateCondition(cond Condition, result model.Record) bool {
	rawValue, exists := result[cond.Field]
	if !exists {
		return false
	}
	value, ok := model.StringValue(rawValue)
	if !ok {
		return false
	}

	switch cond.Operator {
	case "equals":
		return value == cond.Value
	case "not_equals":
		return value != cond.Value
	case "contains":
		return strings.Contains(value, cond.Value)
	case "regex":
		return cond.regex != nil && cond.regex.MatchString(value)
	case "in":
		for _, v := range cond.Values {
			if value == v {
				return true
			}
		}
		return false
	case "not_in":
		for _, v := range cond.Values {
			if value == v {
				return false
			}
		}
		return true
	default:
		// Unsupported operator; treat as non-matching
		return false
	}
}
