package exclusion

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/0x4D31/venator/internal/model"
	"go.yaml.in/yaml/v3"
)

// Condition represents a single condition in an exclusion rule.
type Condition struct {
	Field    string    `yaml:"field"`
	Operator string    `yaml:"operator"`
	Value    *string   `yaml:"value,omitempty"`
	Values   *[]string `yaml:"values,omitempty"`
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

	andPresent bool
	orPresent  bool
}

// UnmarshalYAML retains whether each operator key was written. Slice length
// alone cannot distinguish an omitted key from an explicit empty or null key,
// which would otherwise let a document containing both and/or pass validation.
func (g *ConditionGroup) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("condition group must be a mapping")
	}

	andPresent := false
	orPresent := false
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		switch key {
		case "and":
			if andPresent {
				return fmt.Errorf("condition group contains duplicate key %q", key)
			}
			andPresent = true
		case "or":
			if orPresent {
				return fmt.Errorf("condition group contains duplicate key %q", key)
			}
			orPresent = true
		default:
			return fmt.Errorf("condition group contains unknown key %q", key)
		}
		if err := validateConditionSequence(node.Content[i+1], key); err != nil {
			return err
		}
	}

	type decodedGroup struct {
		And []Condition `yaml:"and,omitempty"`
		Or  []Condition `yaml:"or,omitempty"`
	}
	var decoded decodedGroup
	if err := node.Decode(&decoded); err != nil {
		return err
	}

	g.And = decoded.And
	g.Or = decoded.Or
	g.andPresent = andPresent
	g.orPresent = orPresent
	return nil
}

func validateConditionSequence(node *yaml.Node, operator string) error {
	resolved, err := resolveAliasNode(node)
	if err != nil {
		return err
	}
	if resolved.Kind == yaml.ScalarNode && resolved.Tag == "!!null" {
		return nil
	}
	if resolved.Kind != yaml.SequenceNode {
		return fmt.Errorf("condition group %q must be a sequence", operator)
	}

	for i, conditionNode := range resolved.Content {
		condition, err := resolveAliasNode(conditionNode)
		if err != nil {
			return err
		}
		if condition.Kind != yaml.MappingNode {
			return fmt.Errorf("condition %d in %q must be a mapping", i+1, operator)
		}
		seen := make(map[string]struct{}, len(condition.Content)/2)
		for j := 0; j < len(condition.Content); j += 2 {
			key := condition.Content[j].Value
			switch key {
			case "field", "operator", "value", "values":
			default:
				return fmt.Errorf("condition %d in %q contains unknown key %q", i+1, operator, key)
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("condition %d in %q contains duplicate key %q", i+1, operator, key)
			}
			seen[key] = struct{}{}
		}
	}
	return nil
}

func resolveAliasNode(node *yaml.Node) (*yaml.Node, error) {
	seen := map[*yaml.Node]struct{}{}
	for node != nil && node.Kind == yaml.AliasNode {
		if _, duplicate := seen[node]; duplicate {
			return nil, fmt.Errorf("condition group contains a recursive YAML alias")
		}
		seen[node] = struct{}{}
		node = node.Alias
	}
	if node == nil {
		return nil, fmt.Errorf("condition group contains an invalid YAML alias")
	}
	return node, nil
}

// Excluder manages exclusion rules loaded from a YAML file.
type Excluder struct {
	rules []ExclusionRule
}

// NewExcluder initializes an Excluder by loading rules from a YAML file.
func NewExcluder(path string) (*Excluder, error) {
	file, err := openRegularExclusionFile(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var document yaml.Node
	shapeDecoder := yaml.NewDecoder(file)
	if err := shapeDecoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("failed to decode exclusions YAML: %w", err)
	}
	if err := requireExclusionYAMLEOF(shapeDecoder); err != nil {
		return nil, err
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("failed to decode exclusions YAML: top-level value must be a list")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("failed to rewind exclusions file: %w", err)
	}

	var rules []ExclusionRule
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	if err := decoder.Decode(&rules); err != nil {
		return nil, fmt.Errorf("failed to decode exclusions YAML: %w", err)
	}
	if err := requireExclusionYAMLEOF(decoder); err != nil {
		return nil, err
	}

	for i := range rules {
		if rules[i].Conditions.andPresent && rules[i].Conditions.orPresent {
			return nil, fmt.Errorf("invalid condition group in rule %d: and/or are mutually exclusive", i+1)
		}
		if !rules[i].Conditions.andPresent && !rules[i].Conditions.orPresent {
			return nil, fmt.Errorf("invalid condition group in rule %d: one of and/or is required", i+1)
		}
		if rules[i].Conditions.andPresent && len(rules[i].Conditions.And) == 0 {
			return nil, fmt.Errorf("invalid condition group in rule %d: and must be non-empty", i+1)
		}
		if rules[i].Conditions.orPresent && len(rules[i].Conditions.Or) == 0 {
			return nil, fmt.Errorf("invalid condition group in rule %d: or must be non-empty", i+1)
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

func requireExclusionYAMLEOF(decoder *yaml.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to decode exclusions YAML: %w", err)
	}
	return fmt.Errorf("failed to decode exclusions YAML: multiple YAML documents are not supported")
}

func openRegularExclusionFile(path string) (*os.File, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat exclusions file %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("exclusions file %q must be a regular file", path)
	}
	file, err := os.Open(path) // #nosec G304 -- the rule explicitly selects its exclusion file.
	if err != nil {
		return nil, fmt.Errorf("open exclusions file %q: %w", path, err)
	}
	openedInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect exclusions file %q: %w", path, err)
	}
	if !openedInfo.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("exclusions file %q changed and is no longer a regular file", path)
	}
	return file, nil
}

// validateCondition enforces the operator-specific condition shape and
// precompiles regular expressions.
func validateCondition(cond *Condition) error {
	if strings.TrimSpace(cond.Field) == "" {
		return fmt.Errorf("field is required")
	}
	switch cond.Operator {
	case "equals", "contains", "not_equals":
		if cond.Value == nil {
			return fmt.Errorf("operator %q requires 'value'", cond.Operator)
		}
		if cond.Values != nil {
			return fmt.Errorf("operator %q does not accept 'values'", cond.Operator)
		}
		if cond.Operator == "contains" && *cond.Value == "" {
			return fmt.Errorf("operator %q requires a non-empty 'value'", cond.Operator)
		}
	case "regex":
		if cond.Value == nil || *cond.Value == "" {
			return fmt.Errorf("operator %q requires a non-empty 'value'", cond.Operator)
		}
		if cond.Values != nil {
			return fmt.Errorf("operator %q does not accept 'values'", cond.Operator)
		}
		compiled, err := regexp.Compile(*cond.Value)
		if err != nil {
			return fmt.Errorf("invalid regex pattern %q: %w", *cond.Value, err)
		}
		cond.regex = compiled
	case "in", "not_in":
		if cond.Value != nil {
			return fmt.Errorf("operator %q does not accept 'value'", cond.Operator)
		}
		if cond.Values == nil || len(*cond.Values) == 0 {
			return fmt.Errorf("operator %q requires non-empty 'values'", cond.Operator)
		}
	default:
		return fmt.Errorf("unsupported operator %q", cond.Operator)
	}
	return nil
}

// IsExcluded reports whether any exclusion rule matches result.
func (e *Excluder) IsExcluded(result model.Record) bool {
	for _, rule := range e.rules {
		if evaluateConditionGroup(rule.Conditions, result) {
			return true
		}
	}
	return false
}

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

	return false
}

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
		return cond.Value != nil && value == *cond.Value
	case "not_equals":
		return cond.Value != nil && value != *cond.Value
	case "contains":
		return cond.Value != nil && strings.Contains(value, *cond.Value)
	case "regex":
		return cond.regex != nil && cond.regex.MatchString(value)
	case "in":
		if cond.Values == nil {
			return false
		}
		for _, v := range *cond.Values {
			if value == v {
				return true
			}
		}
		return false
	case "not_in":
		if cond.Values == nil {
			return false
		}
		for _, v := range *cond.Values {
			if value == v {
				return false
			}
		}
		return true
	default:
		return false
	}
}
