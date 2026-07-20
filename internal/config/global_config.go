package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/0x4D31/venator/internal/yamlshape"
	"go.yaml.in/yaml/v3"
)

var (
	environmentReference  = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*\}`)
	connectorInstanceName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

const defaultOpenSearchIndex = "venator-findings-v1"

// GlobalConfig holds the entire global configuration.
type GlobalConfig struct {
	OpenSearch OpenSearchConnectors `yaml:"opensearch"`
	PubSub     PubSubConnectors     `yaml:"pubsub"`
	BigQuery   BigQueryConnectors   `yaml:"bigquery"`
	ClickHouse ClickHouseConnectors `yaml:"clickhouse"`
	Slack      SlackConnectors      `yaml:"slack"`
	Webhook    WebhookConnectors    `yaml:"webhook"`
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

type WebhookConnectors struct {
	Instances map[string]WebhookConfig `yaml:"instances"`
}

type RuntimeConfig struct {
	MaxRecords int      `yaml:"maxRecords"`
	MaxBytes   int64    `yaml:"maxBytes"`
	Timeout    Duration `yaml:"timeout"`
}

type Duration time.Duration

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
		return fmt.Errorf("duration must be a YAML string")
	}
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
	Index              string `yaml:"index,omitempty"`
	SQLFetchSize       int    `yaml:"sqlFetchSize,omitempty"`
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

type WebhookConfig struct {
	URL             string            `yaml:"url"`
	Headers         map[string]string `yaml:"headers,omitempty"`
	SigningSecret   string            `yaml:"signingSecret,omitempty"`
	Timeout         Duration          `yaml:"timeout"`
	MaxAttempts     int               `yaml:"maxAttempts"`
	MaxFindings     int               `yaml:"maxFindings"`
	MaxPayloadBytes int64             `yaml:"maxPayloadBytes"`
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
	Temperature *float64 `yaml:"temperature,omitempty"`
	Timeout     Duration `yaml:"timeout"`
}

// ParseGlobalConfig parses the global YAML configuration file.
func ParseGlobalConfig(path string) (*GlobalConfig, error) {
	var cfg GlobalConfig

	file, err := openRegularConfigFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read global config file: %w", err)
	}
	defer file.Close()
	fileContent, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("failed to read global config file: %w", err)
	}
	var document yaml.Node
	shapeDecoder := yaml.NewDecoder(strings.NewReader(string(fileContent)))
	if err := shapeDecoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("failed to decode global config YAML: %w", err)
	}
	if err := requireYAMLEOF(shapeDecoder); err != nil {
		return nil, fmt.Errorf("failed to decode global config YAML: %w", err)
	}
	if err := yamlshape.RejectMergeKeys(&document); err != nil {
		return nil, fmt.Errorf("failed to decode global config YAML: %w", err)
	}
	if err := yamlshape.RejectAliasMappingKeys(&document); err != nil {
		return nil, fmt.Errorf("failed to decode global config YAML: %w", err)
	}
	if err := yamlshape.ValidateTypes(&document, GlobalConfig{}); err != nil {
		return nil, fmt.Errorf("failed to decode global config YAML: %w", err)
	}

	// Decode before interpolation so secret values can never inject YAML. Each
	// connector resolves its own environment references lazily when selected.
	decoder := yaml.NewDecoder(strings.NewReader(string(fileContent)))
	decoder.KnownFields(true)

	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to decode global config YAML: %w", err)
	}
	if err := requireYAMLEOF(decoder); err != nil {
		return nil, fmt.Errorf("failed to decode global config YAML: %w", err)
	}
	if err := validateEnvironmentReferenceSyntax(&cfg); err != nil {
		return nil, fmt.Errorf("invalid global config: %w", err)
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
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() || reflected.Kind() != reflect.Pointer || reflected.IsNil() {
		return fmt.Errorf("environment resolution requires a non-nil pointer")
	}
	if err := validateEnvironmentReferenceSyntax(value); err != nil {
		return err
	}
	missing := map[string]struct{}{}
	walkEnvironmentValues(reflected, true, missing)
	if len(missing) == 0 {
		return validateResolvedConnectorConfig(value)
	}
	names := make([]string, 0, len(missing))
	for name := range missing {
		names = append(names, name)
	}
	sort.Strings(names)
	return fmt.Errorf("references unset environment variables: %s", strings.Join(names, ", "))
}

func validateResolvedConnectorConfig(value any) error {
	switch cfg := value.(type) {
	case *PubSubConfig:
		return validatePubSubConfig(*cfg)
	case *BigQueryConfig:
		return validateBigQueryConfig(*cfg)
	default:
		return nil
	}
}

func validateEnvironmentReferenceSyntax(value any) error {
	if !walkEnvironmentValues(reflect.ValueOf(value), false, nil) {
		return fmt.Errorf("contains malformed environment reference; use ${NAME}")
	}
	return nil
}

func walkEnvironmentValues(value reflect.Value, expand bool, missing map[string]struct{}) bool {
	if !value.IsValid() {
		return true
	}
	for value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface {
		if value.IsNil() {
			return true
		}
		value = value.Elem()
	}
	switch value.Kind() {
	case reflect.String:
		if hasMalformedEnvironmentReference(value.String()) {
			return false
		}
		if expand && value.CanSet() {
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
			if !walkEnvironmentValues(value.Field(i), expand, missing) {
				return false
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < value.Len(); i++ {
			if !walkEnvironmentValues(value.Index(i), expand, missing) {
				return false
			}
		}
	case reflect.Map:
		iterator := value.MapRange()
		for iterator.Next() {
			entry := iterator.Value()
			if expand {
				copy := reflect.New(value.Type().Elem()).Elem()
				copy.Set(entry)
				entry = copy
			}
			if !walkEnvironmentValues(entry, expand, missing) {
				return false
			}
			if expand {
				value.SetMapIndex(iterator.Key(), entry)
			}
		}
	}
	return true
}

func hasMalformedEnvironmentReference(value string) bool {
	for offset := 0; ; {
		index := strings.Index(value[offset:], "${")
		if index < 0 {
			return false
		}
		index += offset
		match := environmentReference.FindStringIndex(value[index:])
		if match == nil || match[0] != 0 {
			return true
		}
		offset = index + match[1]
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
	for name, instance := range c.OpenSearch.Instances {
		if instance.Index == "" {
			instance.Index = defaultOpenSearchIndex
		}
		c.OpenSearch.Instances[name] = instance
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
	for name, instance := range c.Webhook.Instances {
		if instance.Timeout == 0 {
			instance.Timeout = Duration(20 * time.Second)
		}
		if instance.MaxAttempts == 0 {
			instance.MaxAttempts = 3
		}
		if instance.MaxFindings == 0 {
			instance.MaxFindings = 100
		}
		if instance.MaxPayloadBytes == 0 {
			instance.MaxPayloadBytes = min(int64(1<<20), c.Runtime.MaxBytes)
		}
		c.Webhook.Instances[name] = instance
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
			instance.MaxIdleConns = min(2, instance.MaxOpenConns)
		}
		if instance.Query != nil {
			if instance.Query.Timeout == 0 {
				instance.Query.Timeout = Duration(2 * time.Minute)
			}
			if instance.Query.MaxRows == 0 && c.Runtime.MaxRecords > 0 {
				instance.Query.MaxRows = uint64(c.Runtime.MaxRecords)
			}
			if instance.Query.MaxBytes == 0 && c.Runtime.MaxBytes > 0 {
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
	if c.LLM.Temperature != nil && (math.IsNaN(*c.LLM.Temperature) || math.IsInf(*c.LLM.Temperature, 0) ||
		*c.LLM.Temperature < 0 || *c.LLM.Temperature > 2) {
		return fmt.Errorf("llm.temperature must be between 0 and 2")
	}
	if c.LLM.Timeout.Value() <= 0 {
		return fmt.Errorf("llm.timeout must be positive")
	}
	if err := validateConnectorInstanceNames("opensearch", c.OpenSearch.Instances); err != nil {
		return err
	}
	if err := validateConnectorInstanceNames("pubsub", c.PubSub.Instances); err != nil {
		return err
	}
	if err := validateConnectorInstanceNames("bigquery", c.BigQuery.Instances); err != nil {
		return err
	}
	if err := validateConnectorInstanceNames("slack", c.Slack.Instances); err != nil {
		return err
	}
	if err := validateConnectorInstanceNames("webhook", c.Webhook.Instances); err != nil {
		return err
	}
	if err := validateConnectorInstanceNames("clickhouse", c.ClickHouse.Instances); err != nil {
		return err
	}
	for name, instance := range c.OpenSearch.Instances {
		if strings.TrimSpace(instance.URL) == "" {
			return fmt.Errorf("opensearch instance %q requires url", name)
		}
		if !hasEnvironmentReference(instance.URL) && !validHTTPURL(instance.URL) {
			return fmt.Errorf("opensearch instance %q has invalid url", name)
		}
		if !hasEnvironmentReference(instance.Index) && !validOpenSearchIndex(instance.Index) {
			return fmt.Errorf("opensearch instance %q has invalid index", name)
		}
		if instance.SQLFetchSize < 0 {
			return fmt.Errorf("opensearch instance %q sqlFetchSize cannot be negative", name)
		}
		if instance.SQLFetchSize > c.Runtime.MaxRecords {
			return fmt.Errorf("opensearch instance %q sqlFetchSize cannot exceed runtime.maxRecords (%d)", name, c.Runtime.MaxRecords)
		}
	}
	for name, instance := range c.PubSub.Instances {
		if err := validatePubSubConfig(instance); err != nil {
			return fmt.Errorf("pubsub instance %q %w", name, err)
		}
	}
	for name, instance := range c.BigQuery.Instances {
		if err := validateBigQueryConfig(instance); err != nil {
			return fmt.Errorf("bigquery instance %q %w", name, err)
		}
		if instance.MaxBytesBilled < 1 {
			return fmt.Errorf("bigquery instance %q maxBytesBilled must be positive", name)
		}
	}
	for name, instance := range c.Slack.Instances {
		if strings.TrimSpace(instance.WebhookURL) == "" {
			return fmt.Errorf("slack instance %q requires webhookURL", name)
		}
		if !hasEnvironmentReference(instance.WebhookURL) && !validSlackURL(instance.WebhookURL) {
			return fmt.Errorf("slack instance %q has invalid webhookURL", name)
		}
		if instance.MaxFindings < 1 || instance.MaxFindings > 50 {
			return fmt.Errorf("slack instance %q maxFindings must be between 1 and 50", name)
		}
	}
	for name, instance := range c.Webhook.Instances {
		if err := validateWebhookConfig(instance, c.Runtime.MaxBytes); err != nil {
			return fmt.Errorf("webhook instance %q %w", name, err)
		}
	}
	for name, instance := range c.ClickHouse.Instances {
		if len(instance.Addresses) == 0 {
			return fmt.Errorf("clickhouse instance %q requires at least one address", name)
		}
		if !hasEnvironmentReference(instance.Protocol) && instance.Protocol != "native" && instance.Protocol != "http" {
			return fmt.Errorf("clickhouse instance %q has unsupported protocol %q", name, instance.Protocol)
		}
		if !hasEnvironmentReference(instance.Compression) && instance.Compression != "none" && instance.Compression != "lz4" && instance.Compression != "zstd" {
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
		if instance.Sink != nil && !hasEnvironmentReference(instance.Sink.Table) && !safeTableIdentifier(instance.Sink.Table) {
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

func validateWebhookConfig(cfg WebhookConfig, runtimeMaxBytes int64) error {
	if strings.TrimSpace(cfg.URL) == "" {
		return errors.New("requires url")
	}
	if !hasEnvironmentReference(cfg.URL) && !validWebhookURL(cfg.URL) {
		return errors.New("has invalid url")
	}
	if cfg.Timeout.Value() <= 0 {
		return errors.New("timeout must be positive")
	}
	if cfg.MaxAttempts < 1 || cfg.MaxAttempts > 10 {
		return errors.New("maxAttempts must be between 1 and 10")
	}
	if cfg.MaxFindings < 1 || cfg.MaxFindings > 1000 {
		return errors.New("maxFindings must be between 1 and 1000")
	}
	payloadLimit := min(runtimeMaxBytes, int64(10<<20))
	if cfg.MaxPayloadBytes < 1 || cfg.MaxPayloadBytes > payloadLimit {
		return fmt.Errorf("maxPayloadBytes must be between 1 and %d", payloadLimit)
	}
	if cfg.SigningSecret != "" && !hasEnvironmentReference(cfg.SigningSecret) && !validStandardWebhookSecret(cfg.SigningSecret) {
		return errors.New("signingSecret must be whsec_ followed by base64 encoding 24 to 64 random bytes")
	}
	seenHeaders := make(map[string]struct{}, len(cfg.Headers))
	for name, value := range cfg.Headers {
		if !validWebhookHeaderName(name) {
			return fmt.Errorf("header name %q is invalid or reserved", name)
		}
		lowerName := strings.ToLower(name)
		if _, duplicate := seenHeaders[lowerName]; duplicate {
			return fmt.Errorf("header %q is duplicated with different casing", name)
		}
		if !validWebhookHeaderValue(value) {
			return fmt.Errorf("header %q contains an invalid value", name)
		}
		seenHeaders[lowerName] = struct{}{}
	}
	return nil
}

func validStandardWebhookSecret(value string) bool {
	if strings.TrimSpace(value) != value || !strings.HasPrefix(value, "whsec_") {
		return false
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(strings.TrimPrefix(value, "whsec_"))
	return err == nil && len(decoded) >= 24 && len(decoded) <= 64
}

func validWebhookHeaderName(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		character := value[i]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(character)) {
			continue
		}
		return false
	}
	switch strings.ToLower(value) {
	case "accept", "connection", "content-encoding", "content-length", "content-type", "host", "idempotency-key",
		"proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade",
		"venator-finding-id", "webhook-id", "webhook-signature", "webhook-timestamp":
		return false
	default:
		return true
	}
}

func validWebhookHeaderValue(value string) bool {
	for i := 0; i < len(value); i++ {
		character := value[i]
		if character == '\t' || character >= 0x20 && character != 0x7f {
			continue
		}
		return false
	}
	return true
}

func hasEnvironmentReference(value string) bool {
	return environmentReference.MatchString(value) && !hasMalformedEnvironmentReference(value)
}

func validatePubSubConfig(cfg PubSubConfig) error {
	if strings.TrimSpace(cfg.ProjectID) == "" || strings.TrimSpace(cfg.TopicID) == "" {
		return errors.New("requires projectID and topicID")
	}
	return nil
}

func validateBigQueryConfig(cfg BigQueryConfig) error {
	if strings.TrimSpace(cfg.ProjectID) == "" {
		return errors.New("requires projectID")
	}
	if cfg.DatasetID != "" && strings.TrimSpace(cfg.DatasetID) == "" {
		return errors.New("datasetID cannot be blank")
	}
	if cfg.TableID != "" && strings.TrimSpace(cfg.TableID) == "" {
		return errors.New("tableID cannot be blank")
	}
	if (cfg.DatasetID == "") != (cfg.TableID == "") {
		return errors.New("datasetID and tableID must be configured together")
	}
	return nil
}

func validateConnectorInstanceNames[T any](connector string, instances map[string]T) error {
	names := make([]string, 0, len(instances))
	for name := range instances {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !connectorInstanceName.MatchString(name) {
			return fmt.Errorf("%s instance name %q must contain only letters, digits, hyphens, and underscores", connector, name)
		}
	}
	return nil
}

func validHTTPURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && !parsed.ForceQuery && parsed.Fragment == "" &&
		(parsed.Scheme == "http" || parsed.Scheme == "https")
}

func validOpenSearchIndex(index string) bool {
	if len(index) == 0 || len(index) > 255 {
		return false
	}
	for i := 0; i < len(index); i++ {
		character := index[i]
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') ||
			(i > 0 && (character == '-' || character == '_' || character == '.')) {
			continue
		}
		return false
	}
	return true
}

func validSlackURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	return parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1")
}

func validWebhookURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || strings.TrimSpace(value) != value || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil ||
		parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme == "https" {
		return true
	}
	if scheme != "http" {
		return false
	}
	ip := net.ParseIP(parsed.Hostname())
	return ip != nil && ip.IsLoopback()
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
