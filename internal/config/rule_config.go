package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// RuleConfig represents the entire config structure for a single rule.
type RuleConfig struct {
	Author               string          `yaml:"author"`
	Confidence           ConfidenceLevel `yaml:"confidence"`
	Description          string          `yaml:"description"`
	Enabled              bool            `yaml:"enabled"`
	ExclusionsPath       string          `yaml:"exclusionsPath,omitempty"`
	Language             string          `yaml:"language"`
	LLM                  *LLM            `yaml:"llm,omitempty"`
	Name                 string          `yaml:"name"`
	Output               Output          `yaml:"output"`
	Publishers           []string        `yaml:"publishers"`
	BestEffortPublishers []string        `yaml:"bestEffortPublishers,omitempty"`
	Query                string          `yaml:"query"`
	QueryEngine          string          `yaml:"queryEngine"`
	References           []string        `yaml:"references"`
	Schedule             string          `yaml:"schedule"`
	Status               string          `yaml:"status"`
	Tags                 []string        `yaml:"tags"`
	TTPs                 []TTP           `yaml:"ttps"`
	UID                  string          `yaml:"uid"`
}

type LLM struct {
	Enabled        bool     `yaml:"enabled"`
	Prompt         string   `yaml:"prompt"`
	Required       bool     `yaml:"required,omitempty"`
	MaxFindings    int      `yaml:"maxFindings,omitempty"`
	EvidenceFields []string `yaml:"evidenceFields,omitempty"`
	RedactFields   []string `yaml:"redactFields,omitempty"`
}

type Output struct {
	Format OutputFormat  `yaml:"format"`
	Fields []OutputField `yaml:"fields"`
}

type OutputField struct {
	Field  string `yaml:"field"`
	Source string `yaml:"source"`
}

type TTP struct {
	Framework string `yaml:"framework"`
	Tactic    string `yaml:"tactic"`
	Name      string `yaml:"name"`
	ID        string `yaml:"id"`
	Reference string `yaml:"reference"`
}

type ConfidenceLevel string

const (
	ConfidenceUnknown ConfidenceLevel = "unknown"
	ConfidenceLow     ConfidenceLevel = "low"
	ConfidenceMedium  ConfidenceLevel = "medium"
	ConfidenceHigh    ConfidenceLevel = "high"
)

type OutputFormat string

const (
	OutputFormatRaw    OutputFormat = "raw"
	OutputFormatSignal OutputFormat = "signal"
)

// ParseRuleConfig parses the rules YAML configuration file.
func ParseRuleConfig(path string) (*RuleConfig, error) {
	var cfg RuleConfig
	file, err := openRegularConfigFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open config file: %w", err)
	}
	defer file.Close()

	var document yaml.Node
	shapeDecoder := yaml.NewDecoder(file)
	if err := shapeDecoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("failed to decode config YAML: %w", err)
	}
	if err := requireYAMLEOF(shapeDecoder); err != nil {
		return nil, fmt.Errorf("failed to decode config YAML: %w", err)
	}
	if err := validateRuleScalarTypes(&document); err != nil {
		return nil, fmt.Errorf("failed to decode config YAML: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("failed to rewind config file: %w", err)
	}

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)

	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to decode config YAML: %w", err)
	}
	if err := requireYAMLEOF(decoder); err != nil {
		return nil, fmt.Errorf("failed to decode config YAML: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid rule config: %w", err)
	}
	if cfg.QueryEngine == "file.ndjson" && !filepath.IsAbs(cfg.Query) {
		cfg.Query = filepath.Clean(filepath.Join(filepath.Dir(path), cfg.Query))
	}

	return &cfg, nil
}

func validateRuleScalarTypes(document *yaml.Node) error {
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return nil
	}
	root, err := resolveRuleAlias(document.Content[0])
	if err != nil {
		return err
	}
	if root.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(root.Content); i += 2 {
		key := root.Content[i]
		value, err := resolveRuleAlias(root.Content[i+1])
		if err != nil {
			return err
		}
		switch key.Value {
		case "enabled":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!bool" {
				return fmt.Errorf("enabled must be a YAML boolean")
			}
		case "schedule":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
				return fmt.Errorf("schedule must be a YAML string")
			}
		}
	}
	return nil
}

func resolveRuleAlias(node *yaml.Node) (*yaml.Node, error) {
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

func openRegularConfigFile(path string) (*os.File, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%q is not a regular file", path)
	}
	file, err := os.Open(path) // #nosec G304 -- the CLI explicitly selects this configuration file.
	if err != nil {
		return nil, err
	}
	openedInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("%q changed and is no longer a regular file", path)
	}
	return file, nil
}

func (c *RuleConfig) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(c.UID) == "" {
		return fmt.Errorf("uid is required")
	}
	switch c.Confidence {
	case ConfidenceUnknown, ConfidenceLow, ConfidenceMedium, ConfidenceHigh:
	default:
		return fmt.Errorf("unsupported confidence %q", c.Confidence)
	}
	if strings.TrimSpace(c.QueryEngine) == "" {
		return fmt.Errorf("queryEngine is required")
	}
	if strings.TrimSpace(c.Query) == "" && c.QueryEngine != "stdin.default" {
		return fmt.Errorf("query is required for queryEngine %q", c.QueryEngine)
	}
	if strings.TrimSpace(c.Language) == "" {
		return fmt.Errorf("language is required")
	}
	if err := validateSourceLanguage(c.QueryEngine, c.Language, c.Query); err != nil {
		return err
	}
	if len(c.Publishers)+len(c.BestEffortPublishers) == 0 {
		return fmt.Errorf("at least one publisher is required")
	}
	seen := map[string]struct{}{}
	for _, name := range append(append([]string(nil), c.Publishers...), c.BestEffortPublishers...) {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("publisher name cannot be empty")
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("publisher %q is configured more than once", name)
		}
		seen[name] = struct{}{}
	}
	switch c.Output.Format {
	case OutputFormatRaw:
		if len(c.Output.Fields) != 0 {
			return fmt.Errorf("raw output must not define fields")
		}
	case OutputFormatSignal:
		if len(c.Output.Fields) == 0 {
			return fmt.Errorf("signal output requires at least one field mapping")
		}
		targets := map[string]struct{}{}
		for _, field := range c.Output.Fields {
			if !supportedSignalField(field.Field) {
				return fmt.Errorf("unsupported signal output field %q", field.Field)
			}
			if strings.TrimSpace(field.Source) == "" {
				return fmt.Errorf("signal output field %q requires a source", field.Field)
			}
			if _, duplicate := targets[field.Field]; duplicate {
				return fmt.Errorf("signal output field %q is mapped more than once", field.Field)
			}
			targets[field.Field] = struct{}{}
		}
	default:
		return fmt.Errorf("unsupported output format %q", c.Output.Format)
	}
	if c.LLM != nil && c.LLM.Enabled && strings.TrimSpace(c.LLM.Prompt) == "" {
		return fmt.Errorf("llm.prompt is required when LLM review is enabled")
	}
	if c.LLM != nil && c.LLM.Required && !c.LLM.Enabled {
		return fmt.Errorf("llm.required cannot be true when llm.enabled is false")
	}
	if c.LLM != nil && c.LLM.MaxFindings < 0 {
		return fmt.Errorf("llm.maxFindings cannot be negative")
	}
	if c.LLM != nil {
		for label, fields := range map[string][]string{"evidenceFields": c.LLM.EvidenceFields, "redactFields": c.LLM.RedactFields} {
			seenFields := map[string]struct{}{}
			for _, field := range fields {
				if strings.TrimSpace(field) == "" {
					return fmt.Errorf("llm.%s cannot contain an empty field", label)
				}
				if _, duplicate := seenFields[field]; duplicate {
					return fmt.Errorf("llm.%s contains duplicate field %q", label, field)
				}
				seenFields[field] = struct{}{}
			}
		}
		if len(c.LLM.EvidenceFields) > 0 {
			evidenceFields := make(map[string]struct{}, len(c.LLM.EvidenceFields))
			for _, field := range c.LLM.EvidenceFields {
				evidenceFields[field] = struct{}{}
			}
			for _, field := range c.LLM.RedactFields {
				if _, included := evidenceFields[field]; !included {
					return fmt.Errorf("llm.redactFields field %q must also appear in llm.evidenceFields", field)
				}
			}
		}
	}
	return nil
}

func validateSourceLanguage(queryEngine, language, query string) error {
	language = strings.ToUpper(strings.TrimSpace(language))
	switch {
	case queryEngine == "stdin.default":
		if language != "NDJSON" {
			return fmt.Errorf("queryEngine %q requires language NDJSON", queryEngine)
		}
		if strings.TrimSpace(query) != "" {
			return fmt.Errorf("query must be empty for queryEngine %q", queryEngine)
		}
	case queryEngine == "file.ndjson":
		if language != "NDJSON" {
			return fmt.Errorf("queryEngine %q requires language NDJSON", queryEngine)
		}
	case strings.HasPrefix(queryEngine, "bigquery."), strings.HasPrefix(queryEngine, "clickhouse."):
		if language != "SQL" {
			return fmt.Errorf("queryEngine %q requires language SQL", queryEngine)
		}
	case strings.HasPrefix(queryEngine, "opensearch."):
		if language != "SQL" && language != "PPL" {
			return fmt.Errorf("queryEngine %q requires language SQL or PPL", queryEngine)
		}
	}
	return nil
}

func supportedSignalField(name string) bool {
	switch name {
	case "Timestamp", "ActorUserName", "ActorUserUID", "ResourceName", "ResourceType",
		"ResourceUID", "SrcHostname", "SrcIP", "DstHostname", "DstIP", "Message",
		"EventID", "EventIndex", "RuleSpecificData":
		return true
	default:
		return false
	}
}
