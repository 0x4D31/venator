package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
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
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open config file: %w", err)
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true) // Enforce strict field matching

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
