// Package yamlshape contains structural checks shared by Venator's YAML
// configuration parsers.
package yamlshape

import (
	"errors"

	"go.yaml.in/yaml/v3"
)

// RejectMergeKeys prevents YAML merges from bypassing strict field and scalar
// validation. Ordinary anchors and aliases remain supported.
func RejectMergeKeys(document *yaml.Node) error {
	seen := make(map[*yaml.Node]struct{})
	var walk func(*yaml.Node) error
	walk = func(node *yaml.Node) error {
		if node == nil {
			return nil
		}
		if _, visited := seen[node]; visited {
			return nil
		}
		seen[node] = struct{}{}
		if node.Tag == "!!merge" {
			return errors.New("YAML merge keys are not supported")
		}
		if node.Kind == yaml.AliasNode {
			return walk(node.Alias)
		}
		for _, child := range node.Content {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(document)
}

// RejectAliasMappingKeys prevents aliases from hiding the names of fields from
// structural validation. Aliases remain supported in mapping values and
// sequence elements.
func RejectAliasMappingKeys(document *yaml.Node) error {
	seen := make(map[*yaml.Node]struct{})
	var walk func(*yaml.Node) error
	walk = func(node *yaml.Node) error {
		if node == nil {
			return nil
		}
		if _, visited := seen[node]; visited {
			return nil
		}
		seen[node] = struct{}{}

		if node.Kind == yaml.MappingNode {
			for i := 0; i < len(node.Content); i += 2 {
				if node.Content[i].Kind == yaml.AliasNode {
					return errors.New("YAML aliases are not supported as mapping keys")
				}
			}
		}
		if node.Kind == yaml.AliasNode {
			return walk(node.Alias)
		}
		for _, child := range node.Content {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(document)
}
