package config

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var environmentReference = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*\}`)

// GlobalConfig holds the entire global configuration.
type GlobalConfig struct {
	OpenSearch OpenSearchConnectors `yaml:"opensearch"`
	PubSub     PubSubConnectors     `yaml:"pubsub"`
	BigQuery   BigQueryConnectors   `yaml:"bigquery"`
	ClickHouse ClickHouseConnectors `yaml:"clickhouse"`
	Slack      SlackConnectors      `yaml:"slack"`
	LLM        LLMConfig            `yaml:"llm"`
	Runtime    RuntimeConfig        `yaml:"runtime"`
}

type OpenSearchConnectors struct {
	Instances map[string]OpenSearchConfig `yaml:"instances"`
}

type PubSubConnectors struct {
	Instances map[string]PubSubConfig `yaml:"instances"`
}

type BigQueryConnectors struct {
	Instances map[string]BigQueryConfig `yaml:"instances"`
}

type SlackConnectors struct {
	Instances map[string]SlackConfig `yaml:"instances"`
}

type ClickHouseConnectors struct {
	Instances map[string]ClickHouseConfig `yaml:"instances"`
}

type RuntimeConfig struct {
	MaxRecords int      `yaml:"maxRecords"`
	MaxBytes   int64    `yaml:"maxBytes"`
	Timeout    Duration `yaml:"timeout"`
}

type Duration time.Duration

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	parsed, err := time.ParseDuration(value.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", value.Value, err)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) Value() time.Duration { return time.Duration(d) }

type OpenSearchConfig struct {
	URL                string `yaml:"url"`
	Username           string `yaml:"username,omitempty"`
	Password           string `yaml:"password,omitempty"`
	InsecureSkipVerify bool   `yaml:"insecureSkipVerify"`
}

type PubSubConfig struct {
	ProjectID string `yaml:"projectID"`
	TopicID   string `yaml:"topicID"`
}

type BigQueryConfig struct {
	ProjectID      string `yaml:"projectID"`
	DatasetID      string `yaml:"datasetID"`
	TableID        string `yaml:"tableID"`
	MaxBytesBilled int64  `yaml:"maxBytesBilled"`
}

type SlackConfig struct {
	WebhookURL  string `yaml:"webhookURL,omitempty"`
	MaxFindings int    `yaml:"maxFindings"`
}

type ClickHouseConfig struct {
	Addresses       []string               `yaml:"addresses"`
	Protocol        string                 `yaml:"protocol"`
	Database        string                 `yaml:"database"`
	Username        string                 `yaml:"username"`
	Password        string                 `yaml:"password"`
	Compression     string                 `yaml:"compression"`
	DialTimeout     Duration               `yaml:"dialTimeout"`
	ReadTimeout     Duration               `yaml:"readTimeout"`
	MaxOpenConns    int                    `yaml:"maxOpenConns"`
	MaxIdleConns    int                    `yaml:"maxIdleConns"`
	ConnMaxLifetime Duration               `yaml:"connMaxLifetime"`
	TLS             ClickHouseTLSConfig    `yaml:"tls"`
	Query           *ClickHouseQueryConfig `yaml:"query,omitempty"`
	Sink            *ClickHouseSinkConfig  `yaml:"sink,omitempty"`
}

type ClickHouseTLSConfig struct {
	Enabled            bool   `yaml:"enabled"`
	ServerName         string `yaml:"serverName"`
	CAFile             string `yaml:"caFile"`
	CertFile           string `yaml:"certFile"`
	KeyFile            string `yaml:"keyFile"`
	InsecureSkipVerify bool   `yaml:"insecureSkipVerify"`
}

type ClickHouseQueryConfig struct {
	Timeout  Duration `yaml:"timeout"`
	MaxRows  uint64   `yaml:"maxRows"`
	MaxBytes uint64   `yaml:"maxBytes"`
}

type ClickHouseSinkConfig struct {
	Table string `yaml:"table"`
}

type LLMConfig struct {
	Provider    string   `yaml:"provider"`
	APIKey      string   `yaml:"apiKey"`
	Model       string   `yaml:"model"`
	ServerURL   string   `yaml:"serverURL,omitempty"`
	Temperature float64  `yaml:"temperature"`
	Timeout     Duration `yaml:"timeout"`
}

// ParseGlobalConfig parses the global YAML configuration file.
func ParseGlobalConfig(path string) (*GlobalConfig, error) {
	var cfg GlobalConfig

	fileContent, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read global config file: %w", err)
	}

	// Decode before interpolation so secret values can never inject YAML. Each
	// connector resolves its own environment references lazily when selected.
	decoder := yaml.NewDecoder(strings.NewReader(string(fileContent)))
	decoder.KnownFields(true) // Enforce strict field matching

	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to decode global config YAML: %w", err)
	}
	if err := requireYAMLEOF(decoder); err != nil {
		return nil, fmt.Errorf("failed to decode global config YAML: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid global config: %w", err)
	}

	return &cfg, nil
}

func requireYAMLEOF(decoder *yaml.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("multiple YAML documents are not supported")
}

// ResolveEnv expands environment references after YAML has been decoded. Pass
// a pointer to a connector or LLM config copy; unrelated connectors are left
// unresolved and therefore do not require their credentials in this process.
func ResolveEnv(value any) error {
	missing := map[string]struct{}{}
	resolveEnvValue(reflect.ValueOf(value), missing)
	if len(missing) == 0 {
		return nil
	}
	names := make([]string, 0, len(missing))
	for name := range missing {
		names = append(names, name)
	}
	sort.Strings(names)
	return fmt.Errorf("references unset environment variables: %s", strings.Join(names, ", "))
}

func resolveEnvValue(value reflect.Value, missing map[string]struct{}) {
	if !value.IsValid() {
		return
	}
	if value.Kind() == reflect.Pointer {
		if !value.IsNil() {
			resolveEnvValue(value.Elem(), missing)
		}
		return
	}
	switch value.Kind() {
	case reflect.String:
		if value.CanSet() {
			expanded := environmentReference.ReplaceAllStringFunc(value.String(), func(reference string) string {
				name := reference[2 : len(reference)-1]
				resolved, ok := os.LookupEnv(name)
				if !ok {
					missing[name] = struct{}{}
					return reference
				}
				return resolved
			})
			value.SetString(expanded)
		}
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			resolveEnvValue(value.Field(i), missing)
		}
	case reflect.Slice:
		for i := 0; i < value.Len(); i++ {
			resolveEnvValue(value.Index(i), missing)
		}
	case reflect.Map:
		iterator := value.MapRange()
		for iterator.Next() {
			entry := reflect.New(value.Type().Elem()).Elem()
			entry.Set(iterator.Value())
			resolveEnvValue(entry, missing)
			value.SetMapIndex(iterator.Key(), entry)
		}
	}
}

func (c *GlobalConfig) applyDefaults() {
	if c.Runtime.MaxRecords == 0 {
		c.Runtime.MaxRecords = 10_000
	}
	if c.Runtime.MaxBytes == 0 {
		c.Runtime.MaxBytes = 64 << 20
	}
	if c.Runtime.Timeout == 0 {
		c.Runtime.Timeout = Duration(15 * time.Minute)
	}
	if c.LLM.Timeout == 0 {
		c.LLM.Timeout = Duration(30 * time.Second)
	}
	for name, instance := range c.BigQuery.Instances {
		if instance.MaxBytesBilled == 0 {
			instance.MaxBytesBilled = 10 << 30
		}
		c.BigQuery.Instances[name] = instance
	}
	for name, instance := range c.Slack.Instances {
		if instance.MaxFindings == 0 {
			instance.MaxFindings = 20
		}
		c.Slack.Instances[name] = instance
	}
	for name, instance := range c.ClickHouse.Instances {
		if instance.Protocol == "" {
			instance.Protocol = "native"
		}
		if instance.Compression == "" {
			instance.Compression = "lz4"
		}
		if instance.DialTimeout == 0 {
			instance.DialTimeout = Duration(5 * time.Second)
		}
		if instance.ReadTimeout == 0 {
			instance.ReadTimeout = Duration(2 * time.Minute)
		}
		if instance.ConnMaxLifetime == 0 {
			instance.ConnMaxLifetime = Duration(30 * time.Minute)
		}
		if instance.MaxOpenConns == 0 {
			instance.MaxOpenConns = 4
		}
		if instance.MaxIdleConns == 0 {
			instance.MaxIdleConns = 2
		}
		if instance.Query != nil {
			if instance.Query.Timeout == 0 {
				instance.Query.Timeout = Duration(2 * time.Minute)
			}
			if instance.Query.MaxRows == 0 {
				instance.Query.MaxRows = uint64(c.Runtime.MaxRecords)
			}
			if instance.Query.MaxBytes == 0 {
				instance.Query.MaxBytes = uint64(c.Runtime.MaxBytes)
			}
		}
		c.ClickHouse.Instances[name] = instance
	}
}

func (c *GlobalConfig) Validate() error {
	if c.Runtime.MaxRecords < 1 {
		return fmt.Errorf("runtime.maxRecords must be positive")
	}
	if c.Runtime.MaxBytes < 1 {
		return fmt.Errorf("runtime.maxBytes must be positive")
	}
	if c.Runtime.Timeout.Value() <= 0 {
		return fmt.Errorf("runtime.timeout must be positive")
	}
	if c.LLM.Temperature < 0 || c.LLM.Temperature > 2 {
		return fmt.Errorf("llm.temperature must be between 0 and 2")
	}
	if c.LLM.Timeout.Value() <= 0 {
		return fmt.Errorf("llm.timeout must be positive")
	}
	for name, instance := range c.OpenSearch.Instances {
		if strings.TrimSpace(instance.URL) == "" {
			return fmt.Errorf("opensearch instance %q requires url", name)
		}
		if !validHTTPURL(instance.URL) {
			return fmt.Errorf("opensearch instance %q has invalid url", name)
		}
	}
	for name, instance := range c.PubSub.Instances {
		if instance.ProjectID == "" || instance.TopicID == "" {
			return fmt.Errorf("pubsub instance %q requires projectID and topicID", name)
		}
	}
	for name, instance := range c.BigQuery.Instances {
		if instance.ProjectID == "" {
			return fmt.Errorf("bigquery instance %q requires projectID", name)
		}
		if (instance.DatasetID == "") != (instance.TableID == "") {
			return fmt.Errorf("bigquery instance %q datasetID and tableID must be configured together", name)
		}
		if instance.MaxBytesBilled < 1 {
			return fmt.Errorf("bigquery instance %q maxBytesBilled must be positive", name)
		}
	}
	for name, instance := range c.Slack.Instances {
		if strings.TrimSpace(instance.WebhookURL) == "" {
			return fmt.Errorf("slack instance %q requires webhookURL", name)
		}
		if !validSlackURL(instance.WebhookURL) {
			return fmt.Errorf("slack instance %q has invalid webhookURL", name)
		}
		if instance.MaxFindings < 1 || instance.MaxFindings > 50 {
			return fmt.Errorf("slack instance %q maxFindings must be between 1 and 50", name)
		}
	}
	for name, instance := range c.ClickHouse.Instances {
		if len(instance.Addresses) == 0 {
			return fmt.Errorf("clickhouse instance %q requires at least one address", name)
		}
		if instance.Protocol != "native" && instance.Protocol != "http" {
			return fmt.Errorf("clickhouse instance %q has unsupported protocol %q", name, instance.Protocol)
		}
		if instance.Compression != "none" && instance.Compression != "lz4" && instance.Compression != "zstd" {
			return fmt.Errorf("clickhouse instance %q has unsupported compression %q", name, instance.Compression)
		}
		if instance.Query == nil && instance.Sink == nil {
			return fmt.Errorf("clickhouse instance %q must configure query, sink, or both", name)
		}
		if instance.MaxOpenConns < 1 || instance.MaxIdleConns < 1 || instance.MaxIdleConns > instance.MaxOpenConns {
			return fmt.Errorf("clickhouse instance %q has invalid connection pool limits", name)
		}
		if instance.DialTimeout.Value() <= 0 || instance.ReadTimeout.Value() <= 0 || instance.ConnMaxLifetime.Value() <= 0 {
			return fmt.Errorf("clickhouse instance %q timeouts and connection lifetime must be positive", name)
		}
		if instance.Query != nil && (instance.Query.Timeout.Value() <= 0 || instance.Query.MaxRows == 0) {
			return fmt.Errorf("clickhouse instance %q query timeout and maxRows must be positive", name)
		}
		if instance.Query != nil && instance.Query.MaxRows > uint64(c.Runtime.MaxRecords) {
			return fmt.Errorf("clickhouse instance %q query.maxRows cannot exceed runtime.maxRecords (%d)", name, c.Runtime.MaxRecords)
		}
		if instance.Query != nil && instance.Query.MaxBytes > uint64(c.Runtime.MaxBytes) {
			return fmt.Errorf("clickhouse instance %q query.maxBytes cannot exceed runtime.maxBytes (%d)", name, c.Runtime.MaxBytes)
		}
		if instance.Sink != nil && strings.TrimSpace(instance.Sink.Table) == "" {
			return fmt.Errorf("clickhouse instance %q sink.table is required", name)
		}
		if instance.Sink != nil && !safeTableIdentifier(instance.Sink.Table) {
			return fmt.Errorf("clickhouse instance %q sink.table must be table or database.table using letters, digits, and underscores", name)
		}
		if (instance.TLS.CertFile == "") != (instance.TLS.KeyFile == "") {
			return fmt.Errorf("clickhouse instance %q TLS certFile and keyFile must be configured together", name)
		}
		if !instance.TLS.Enabled && (instance.TLS.ServerName != "" || instance.TLS.CAFile != "" || instance.TLS.CertFile != "" || instance.TLS.KeyFile != "" || instance.TLS.InsecureSkipVerify) {
			return fmt.Errorf("clickhouse instance %q sets TLS options while tls.enabled is false", name)
		}
	}
	return nil
}

func validHTTPURL(value string) bool {
	if strings.Contains(value, "${") {
		return true
	}
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && parsed.Host != "" && parsed.User == nil && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func validSlackURL(value string) bool {
	if strings.Contains(value, "${") {
		return true
	}
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	return parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1")
}

func safeTableIdentifier(table string) bool {
	parts := strings.Split(table, ".")
	if len(parts) < 1 || len(parts) > 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for i, character := range part {
			if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || character == '_' || (i > 0 && character >= '0' && character <= '9') {
				continue
			}
			return false
		}
	}
	return true
}
