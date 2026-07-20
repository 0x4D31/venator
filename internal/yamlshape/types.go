package yamlshape

import (
	"fmt"
	"reflect"
	"strings"

	"go.yaml.in/yaml/v3"
)

var nodeUnmarshalerType = reflect.TypeOf((*interface {
	UnmarshalYAML(*yaml.Node) error
})(nil)).Elem()

// ValidateTypes rejects YAML values that the decoder would otherwise coerce
// into the Go type of target. Required fields and unknown keys remain the
// responsibility of the normal strict decoder and config validation.
func ValidateTypes(document *yaml.Node, target any) error {
	if target == nil {
		return fmt.Errorf("YAML type target is nil")
	}
	targetType := reflect.TypeOf(target)
	for targetType.Kind() == reflect.Pointer {
		targetType = targetType.Elem()
	}

	node := document
	if node != nil && node.Kind == yaml.DocumentNode {
		if len(node.Content) != 1 {
			return nil
		}
		node = node.Content[0]
	}
	return validateNodeType(node, targetType, "document")
}

func validateNodeType(node *yaml.Node, target reflect.Type, path string) error {
	var err error
	node, err = resolveAlias(node)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	for target.Kind() == reflect.Pointer {
		if node.Tag == "!!null" {
			return fmt.Errorf("%s must not be null", path)
		}
		target = target.Elem()
	}

	if target.Kind() != reflect.Struct && reflect.PointerTo(target).Implements(nodeUnmarshalerType) {
		if node.Tag == "!!null" {
			return fmt.Errorf("%s must not be null", path)
		}
		return nil
	}

	switch target.Kind() {
	case reflect.Struct:
		if node.Kind != yaml.MappingNode {
			return fmt.Errorf("%s must be a YAML mapping", path)
		}
		fields := yamlFields(target)
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return fmt.Errorf("%s mapping keys must be YAML strings", path)
			}
			field, known := fields[key.Value]
			if !known {
				continue
			}
			if err := validateNodeType(node.Content[i+1], field, joinPath(path, key.Value)); err != nil {
				return err
			}
		}
		return nil
	case reflect.Map:
		if node.Kind != yaml.MappingNode {
			return fmt.Errorf("%s must be a YAML mapping", path)
		}
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if err := validateNodeType(key, target.Key(), path+" key"); err != nil {
				return err
			}
			if err := validateNodeType(node.Content[i+1], target.Elem(), joinPath(path, key.Value)); err != nil {
				return err
			}
		}
		return nil
	case reflect.Slice, reflect.Array:
		if node.Kind != yaml.SequenceNode {
			return fmt.Errorf("%s must be a YAML sequence", path)
		}
		for i, child := range node.Content {
			if err := validateNodeType(child, target.Elem(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		return nil
	case reflect.String:
		return requireScalarTag(node, "!!str", path, "string")
	case reflect.Bool:
		return requireScalarTag(node, "!!bool", path, "boolean")
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return requireScalarTag(node, "!!int", path, "integer")
	case reflect.Float32, reflect.Float64:
		if node.Kind != yaml.ScalarNode || (node.Tag != "!!float" && node.Tag != "!!int") {
			return fmt.Errorf("%s must be a YAML number", path)
		}
		return nil
	case reflect.Interface:
		return nil
	default:
		return nil
	}
}

func yamlFields(target reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type, target.NumField())
	for i := 0; i < target.NumField(); i++ {
		field := target.Field(i)
		if !field.IsExported() {
			continue
		}
		name := strings.Split(field.Tag.Get("yaml"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = strings.ToLower(field.Name)
		}
		fields[name] = field.Type
	}
	return fields
}

func requireScalarTag(node *yaml.Node, tag, path, name string) error {
	if node.Kind != yaml.ScalarNode || node.Tag != tag {
		return fmt.Errorf("%s must be a YAML %s", path, name)
	}
	return nil
}

func resolveAlias(node *yaml.Node) (*yaml.Node, error) {
	seen := make(map[*yaml.Node]struct{})
	for node != nil && node.Kind == yaml.AliasNode {
		if _, duplicate := seen[node]; duplicate {
			return nil, fmt.Errorf("recursive YAML alias")
		}
		seen[node] = struct{}{}
		node = node.Alias
	}
	if node == nil {
		return nil, fmt.Errorf("invalid YAML alias")
	}
	return node, nil
}

func joinPath(parent, child string) string {
	if parent == "document" {
		return child
	}
	return parent + "." + child
}
