package connector

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/0x4D31/venator/connector/bigquery"
	clickhouseconnector "github.com/0x4D31/venator/connector/clickhouse"
	"github.com/0x4D31/venator/connector/opensearch"
	"github.com/0x4D31/venator/connector/pubsub"
	"github.com/0x4D31/venator/connector/slack"
	"github.com/0x4D31/venator/connector/stdio"
	"github.com/0x4D31/venator/internal/config"
)

type queryFactory func(context.Context) (QueryRunner, error)
type publisherFactory func(context.Context) (Publisher, error)
type connectorValidator func(context.Context) error

// Registry lazily constructs only the connectors referenced by a rule. This
// keeps unrelated credentials and services from blocking a local one-shot run.
type Registry struct {
	ctx                 context.Context
	mu                  sync.Mutex
	queryFactories      map[string]queryFactory
	publisherFactories  map[string]publisherFactory
	queryValidators     map[string]connectorValidator
	publisherValidators map[string]connectorValidator
	queryRunners        map[string]QueryRunner
	publishers          map[string]Publisher
	closers             []Closer
}

func NewRegistry(ctx context.Context, globalCfg *config.GlobalConfig, stdin io.Reader, stdout io.Writer) *Registry {
	r := &Registry{
		ctx:            ctx,
		queryFactories: make(map[string]queryFactory), publisherFactories: make(map[string]publisherFactory),
		queryValidators: make(map[string]connectorValidator), publisherValidators: make(map[string]connectorValidator),
		queryRunners: make(map[string]QueryRunner), publishers: make(map[string]Publisher),
	}
	r.queryRunners["stdin.default"] = stdio.NewSource(stdin, globalCfg.Runtime.MaxRecords, globalCfg.Runtime.MaxBytes)
	r.queryRunners["file.ndjson"] = stdio.NewFileSource(globalCfg.Runtime.MaxRecords, globalCfg.Runtime.MaxBytes)
	r.publishers["stdout.default"] = stdio.NewSink(stdout)
	r.registerOpenSearch(globalCfg.OpenSearch, globalCfg.Runtime.MaxRecords, globalCfg.Runtime.MaxBytes)
	r.registerPubSub(globalCfg.PubSub)
	r.registerBigQuery(globalCfg.BigQuery, globalCfg.Runtime.MaxRecords, globalCfg.Runtime.MaxBytes)
	r.registerClickHouse(globalCfg.ClickHouse)
	r.registerSlack(globalCfg.Slack)
	return r
}

func (r *Registry) registerClickHouse(connectors config.ClickHouseConnectors) {
	for name, value := range connectors.Instances {
		cfg := value
		instance := "clickhouse." + name
		var once sync.Once
		var client *clickhouseconnector.Client
		var openErr error
		open := func(ctx context.Context) (*clickhouseconnector.Client, error) {
			once.Do(func() {
				if openErr = config.ResolveEnv(&cfg); openErr == nil {
					client, openErr = clickhouseconnector.Open(ctx, cfg)
				}
			})
			return client, openErr
		}
		validate := func(ctx context.Context) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			resolved := cfg
			if err := config.ResolveEnv(&resolved); err != nil {
				return err
			}
			return clickhouseconnector.ValidateConfig(resolved)
		}
		if cfg.Query != nil {
			r.queryValidators[instance] = validate
			r.queryFactories[instance] = func(ctx context.Context) (QueryRunner, error) {
				client, err := open(ctx)
				if err != nil {
					return nil, err
				}
				return client.Source()
			}
		}
		if cfg.Sink != nil {
			r.publisherValidators[instance] = validate
			r.publisherFactories[instance] = func(ctx context.Context) (Publisher, error) {
				client, err := open(ctx)
				if err != nil {
					return nil, err
				}
				return client.Sink()
			}
		}
	}
}

func (r *Registry) registerOpenSearch(connectors config.OpenSearchConnectors, maxRows int, maxBytes int64) {
	for name, value := range connectors.Instances {
		cfg := value
		instance := "opensearch." + name
		factory := func(ctx context.Context) (*opensearch.Client, error) {
			resolved := cfg
			if err := config.ResolveEnv(&resolved); err != nil {
				return nil, err
			}
			if resolved.URL == "" {
				return nil, fmt.Errorf("OpenSearch URL is required")
			}
			return opensearch.New(ctx, opensearch.Config{
				URL: resolved.URL, Username: resolved.Username, Password: resolved.Password,
				InsecureSkipVerify: resolved.InsecureSkipVerify, MaxRows: maxRows,
				MaxBytes: maxBytes,
			})
		}
		validate := func(ctx context.Context) error {
			client, err := factory(ctx)
			if err != nil {
				return err
			}
			return client.Close()
		}
		r.queryValidators[instance] = validate
		r.publisherValidators[instance] = validate
		r.queryFactories[instance] = func(ctx context.Context) (QueryRunner, error) { return factory(ctx) }
		r.publisherFactories[instance] = func(ctx context.Context) (Publisher, error) { return factory(ctx) }
	}
}

func (r *Registry) registerPubSub(connectors config.PubSubConnectors) {
	for name, value := range connectors.Instances {
		cfg := value
		instance := "pubsub." + name
		factory := func(ctx context.Context) (*pubsub.Client, error) {
			resolved := cfg
			if err := config.ResolveEnv(&resolved); err != nil {
				return nil, err
			}
			if resolved.ProjectID == "" || resolved.TopicID == "" {
				return nil, fmt.Errorf("Pub/Sub projectID and topicID are required")
			}
			return pubsub.New(ctx, pubsub.Config{ProjectID: resolved.ProjectID, TopicID: resolved.TopicID})
		}
		r.publisherValidators[instance] = func(ctx context.Context) error {
			_, err := factory(ctx)
			return err
		}
		r.publisherFactories[instance] = func(ctx context.Context) (Publisher, error) { return factory(ctx) }
	}
}

func (r *Registry) registerBigQuery(connectors config.BigQueryConnectors, maxRows int, maxBytes int64) {
	for name, value := range connectors.Instances {
		cfg := value
		instance := "bigquery." + name
		resolve := func() (config.BigQueryConfig, error) {
			resolved := cfg
			if err := config.ResolveEnv(&resolved); err != nil {
				return resolved, err
			}
			if resolved.ProjectID == "" {
				return resolved, fmt.Errorf("BigQuery projectID is required")
			}
			if (resolved.DatasetID == "") != (resolved.TableID == "") {
				return resolved, fmt.Errorf("BigQuery datasetID and tableID must be configured together")
			}
			return resolved, nil
		}
		resolveForSink := func() (config.BigQueryConfig, error) {
			resolved, err := resolve()
			if err != nil {
				return resolved, err
			}
			if resolved.DatasetID == "" || resolved.TableID == "" {
				return resolved, fmt.Errorf("BigQuery sink requires non-empty datasetID and tableID")
			}
			return resolved, nil
		}
		validate := func(ctx context.Context) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			_, err := resolve()
			return err
		}
		r.queryValidators[instance] = validate
		r.queryFactories[instance] = func(ctx context.Context) (QueryRunner, error) {
			resolved, err := resolve()
			if err != nil {
				return nil, err
			}
			return bigquery.New(ctx, bigquery.Config{ProjectID: resolved.ProjectID, MaxRows: maxRows, MaxBytesBilled: resolved.MaxBytesBilled, MaxResultBytes: maxBytes})
		}
		if cfg.DatasetID != "" && cfg.TableID != "" {
			r.publisherValidators[instance] = func(ctx context.Context) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				_, err := resolveForSink()
				return err
			}
			r.publisherFactories[instance] = func(ctx context.Context) (Publisher, error) {
				resolved, err := resolveForSink()
				if err != nil {
					return nil, err
				}
				return bigquery.New(ctx, bigquery.Config{ProjectID: resolved.ProjectID, DatasetID: resolved.DatasetID, TableID: resolved.TableID, MaxRows: maxRows, MaxBytesBilled: resolved.MaxBytesBilled, MaxResultBytes: maxBytes})
			}
		}
	}
}

func (r *Registry) registerSlack(connectors config.SlackConnectors) {
	for name, value := range connectors.Instances {
		cfg := value
		instance := "slack." + name
		factory := func(ctx context.Context) (*slack.Client, error) {
			resolved := cfg
			if err := config.ResolveEnv(&resolved); err != nil {
				return nil, err
			}
			if resolved.WebhookURL == "" {
				return nil, fmt.Errorf("slack webhookURL is required")
			}
			return slack.New(ctx, slack.Config{WebhookURL: resolved.WebhookURL, MaxFindings: resolved.MaxFindings})
		}
		r.publisherValidators[instance] = func(ctx context.Context) error {
			_, err := factory(ctx)
			return err
		}
		r.publisherFactories[instance] = func(ctx context.Context) (Publisher, error) { return factory(ctx) }
	}
}

func (r *Registry) GetQueryRunner(name string) (QueryRunner, error) {
	r.mu.Lock()
	if source, ok := r.queryRunners[name]; ok {
		r.mu.Unlock()
		return source, nil
	}
	factory, ok := r.queryFactories[name]
	r.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("query runner %q not found", name)
	}
	source, err := factory(r.ctx)
	if err != nil {
		return nil, fmt.Errorf("initialize query runner %q: %w", name, err)
	}
	r.mu.Lock()
	if existing, ok := r.queryRunners[name]; ok {
		r.mu.Unlock()
		if closer, ok := source.(Closer); ok {
			_ = closer.Close()
		}
		return existing, nil
	}
	r.queryRunners[name] = source
	r.trackCloser(source)
	r.mu.Unlock()
	return source, nil
}

func (r *Registry) GetPublisher(name string) (Publisher, error) {
	r.mu.Lock()
	if sink, ok := r.publishers[name]; ok {
		r.mu.Unlock()
		return sink, nil
	}
	factory, ok := r.publisherFactories[name]
	r.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("publisher %q not found", name)
	}
	sink, err := factory(r.ctx)
	if err != nil {
		return nil, fmt.Errorf("initialize publisher %q: %w", name, err)
	}
	r.mu.Lock()
	if existing, ok := r.publishers[name]; ok {
		r.mu.Unlock()
		if closer, ok := sink.(Closer); ok {
			_ = closer.Close()
		}
		return existing, nil
	}
	r.publishers[name] = sink
	r.trackCloser(sink)
	r.mu.Unlock()
	return sink, nil
}

// ValidateReferences checks connector names and roles without resolving secrets.
func (r *Registry) ValidateReferences(rule *config.RuleConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rule == nil {
		return fmt.Errorf("rule is nil")
	}
	if _, ready := r.queryRunners[rule.QueryEngine]; !ready {
		if _, configured := r.queryFactories[rule.QueryEngine]; !configured {
			return fmt.Errorf("query runner %q is not configured", rule.QueryEngine)
		}
	}
	for _, name := range append(append([]string(nil), rule.Publishers...), rule.BestEffortPublishers...) {
		if _, ready := r.publishers[name]; ready {
			continue
		}
		if _, configured := r.publisherFactories[name]; !configured {
			return fmt.Errorf("publisher %q is not configured", name)
		}
	}
	return nil
}

// PreflightRule validates the selected source and required sinks locally,
// before a query runs, without opening network connections. Best-effort sinks
// retain their failure-isolation semantics and are checked by ValidateRule.
func (r *Registry) PreflightRule(rule *config.RuleConfig) error {
	return r.preflightRule(rule, false)
}

// ValidateRule performs the complete local preflight used by the validate CLI,
// including best-effort sinks. It never pings remote services.
func (r *Registry) ValidateRule(rule *config.RuleConfig) error {
	return r.preflightRule(rule, true)
}

func (r *Registry) preflightRule(rule *config.RuleConfig, includeBestEffort bool) error {
	if err := r.ValidateReferences(rule); err != nil {
		return err
	}
	r.mu.Lock()
	queryValidator := r.queryValidators[rule.QueryEngine]
	publishers := append([]string(nil), rule.Publishers...)
	if includeBestEffort {
		publishers = append(publishers, rule.BestEffortPublishers...)
	}
	publisherValidators := make(map[string]connectorValidator, len(publishers))
	for _, name := range publishers {
		publisherValidators[name] = r.publisherValidators[name]
	}
	r.mu.Unlock()
	if queryValidator != nil {
		if err := queryValidator(r.ctx); err != nil {
			return fmt.Errorf("preflight query runner %q: %w", rule.QueryEngine, err)
		}
	}
	for _, name := range publishers {
		if validator := publisherValidators[name]; validator != nil {
			if err := validator(r.ctx); err != nil {
				return fmt.Errorf("preflight publisher %q: %w", name, err)
			}
		}
	}
	return nil
}

func (r *Registry) trackCloser(value any) {
	if closer, ok := value.(Closer); ok {
		r.closers = append(r.closers, closer)
	}
}

func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	errs := make([]error, 0, len(r.closers))
	for i := len(r.closers) - 1; i >= 0; i-- {
		if err := r.closers[i].Close(); err != nil {
			errs = append(errs, err)
		}
	}
	r.closers = nil
	return errors.Join(errs...)
}
