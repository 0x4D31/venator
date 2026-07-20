// Package exclusion loads and evaluates named CEL exclusion expressions.
package exclusion

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/0x4D31/venator/internal/predicate"
	"github.com/0x4D31/venator/internal/yamlshape"
	"go.yaml.in/yaml/v3"
)

const (
	maxExclusions     = 256
	maxNameCodePoints = 128
)

type definition struct {
	Name string `yaml:"name"`
	When string `yaml:"when"`
}

type compiledExclusion struct {
	name      string
	predicate *predicate.Predicate
}

// Excluder evaluates a set of named exclusion expressions.
type Excluder struct {
	exclusions []compiledExclusion
}

// NewExcluder loads and compiles exclusions from path.
func NewExcluder(path string) (*Excluder, error) {
	file, err := openRegularFile(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var document yaml.Node
	decoder := yaml.NewDecoder(file)
	if err := decoder.Decode(&document); err != nil {
		if err == io.EOF {
			return nil, decodeError(fmt.Errorf("top-level value must be a list"))
		}
		return nil, decodeError(err)
	}
	if err := requireEOF(decoder); err != nil {
		return nil, err
	}
	if err := yamlshape.RejectMergeKeys(&document); err != nil {
		return nil, decodeError(err)
	}
	if err := yamlshape.RejectAliasMappingKeys(&document); err != nil {
		return nil, decodeError(err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.SequenceNode {
		return nil, decodeError(fmt.Errorf("top-level value must be a list"))
	}
	sequence := document.Content[0]
	if len(sequence.Content) > maxExclusions {
		return nil, decodeError(fmt.Errorf("at most %d exclusions are allowed", maxExclusions))
	}
	if err := validateEntryShape(sequence); err != nil {
		return nil, decodeError(err)
	}
	if err := yamlshape.ValidateTypes(&document, []definition{}); err != nil {
		return nil, decodeError(err)
	}

	var definitions []definition
	if err := sequence.Decode(&definitions); err != nil {
		return nil, decodeError(err)
	}

	seenNames := make(map[string]struct{}, len(definitions))
	compiled := make([]compiledExclusion, 0, len(definitions))
	for i, definition := range definitions {
		position := i + 1
		if strings.TrimSpace(definition.Name) == "" {
			return nil, fmt.Errorf("invalid exclusion %d: name cannot be blank", position)
		}
		if strings.TrimSpace(definition.Name) != definition.Name {
			return nil, fmt.Errorf("invalid exclusion %d: name must not have leading or trailing whitespace", position)
		}
		if strings.IndexFunc(definition.Name, unicode.IsControl) >= 0 {
			return nil, fmt.Errorf("invalid exclusion %d: name must not contain control characters", position)
		}
		if utf8.RuneCountInString(definition.Name) > maxNameCodePoints {
			return nil, fmt.Errorf("invalid exclusion %d: name must not exceed %d Unicode code points", position, maxNameCodePoints)
		}
		if _, duplicate := seenNames[definition.Name]; duplicate {
			return nil, fmt.Errorf("invalid exclusion %d: duplicate name %q", position, definition.Name)
		}
		seenNames[definition.Name] = struct{}{}
		if strings.TrimSpace(definition.When) == "" {
			return nil, fmt.Errorf("invalid exclusion %q: when cannot be blank", definition.Name)
		}
		compiledPredicate, err := predicate.Compile(definition.When)
		if err != nil {
			return nil, fmt.Errorf("compile exclusion %q: %w", definition.Name, err)
		}
		compiled = append(compiled, compiledExclusion{
			name:      definition.Name,
			predicate: compiledPredicate,
		})
	}

	return &Excluder{exclusions: compiled}, nil
}

// IsExcluded reports whether any exclusion matches a prepared event.
func (e *Excluder) IsExcluded(ctx context.Context, event map[string]any) (bool, error) {
	for _, exclusion := range e.exclusions {
		matched, err := exclusion.predicate.MatchPrepared(ctx, event)
		if err != nil {
			return false, fmt.Errorf("evaluate exclusion %q: %w", exclusion.name, err)
		}
		if matched {
			return true, nil
		}
	}
	return false, nil
}

func validateEntryShape(sequence *yaml.Node) error {
	for i, entryNode := range sequence.Content {
		entry, err := resolveAlias(entryNode)
		if err != nil {
			return fmt.Errorf("exclusion %d: %w", i+1, err)
		}
		if entry.Kind != yaml.MappingNode {
			return fmt.Errorf("exclusion %d must be a mapping", i+1)
		}

		seen := make(map[string]struct{}, len(entry.Content)/2)
		for j := 0; j < len(entry.Content); j += 2 {
			key := entry.Content[j]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return fmt.Errorf("exclusion %d mapping keys must be strings", i+1)
			}
			switch key.Value {
			case "name", "when":
			default:
				return fmt.Errorf("exclusion %d contains unknown key %q", i+1, key.Value)
			}
			if _, duplicate := seen[key.Value]; duplicate {
				return fmt.Errorf("exclusion %d contains duplicate key %q", i+1, key.Value)
			}
			seen[key.Value] = struct{}{}
		}
	}
	return nil
}

func resolveAlias(node *yaml.Node) (*yaml.Node, error) {
	seen := map[*yaml.Node]struct{}{}
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

func requireEOF(decoder *yaml.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return decodeError(err)
	}
	return decodeError(fmt.Errorf("multiple YAML documents are not supported"))
}

func decodeError(err error) error {
	return fmt.Errorf("failed to decode exclusions YAML: %w", err)
}

func openRegularFile(path string) (*os.File, error) {
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
