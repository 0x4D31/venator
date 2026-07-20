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
	"github.com/0x4D31/venator/connector/webhook"
	"github.com/0x4D31/venator/internal/config"
)

type queryFactory func(context.Context) (QueryRunner, error)
type publisherFactory func(context.Context) (Publisher, error)
type connectorValidator func(context.Context) error

type queryInitialization struct {
	done   chan struct{}
	source QueryRunner
	err    error
}

type publisherInitialization struct {
	done chan struct{}
	sink Publisher
	err  error
}

// Registry lazily constructs only the connectors referenced by a rule. This
// keeps unrelated credentials and services from blocking a local one-shot run.
type Registry struct {
	ctx                       context.Context
	mu                        sync.Mutex
	queryFactories            map[string]queryFactory
	publisherFactories        map[string]publisherFactory
	queryValidators           map[string]connectorValidator
	publisherValidators       map[string]connectorValidator
	queryRunners              map[string]QueryRunner
	publishers                map[string]Publisher
	queryInitializations      map[string]*queryInitialization
	publisherInitializations  map[string]*publisherInitialization
	closers                   []Closer
	initializationCleanupErrs []error
	initializing              sync.WaitGroup
	closeOnce                 sync.Once
	closeErr                  error
	closed                    bool
}

func NewRegistry(ctx context.Context, globalCfg *config.GlobalConfig, stdin io.Reader, stdout io.Writer) *Registry {
	r := &Registry{
		ctx:            ctx,
		queryFactories: make(map[string]queryFactory), publisherFactories: make(map[string]publisherFactory),
		queryValidators: make(map[string]connectorValidator), publisherValidators: make(map[string]connectorValidator),
		queryRunners: make(map[string]QueryRunner), publishers: make(map[string]Publisher),
		queryInitializations:     make(map[string]*queryInitialization),
		publisherInitializations: make(map[string]*publisherInitialization),
	}
	r.queryRunners["stdin.default"] = stdio.NewSource(stdin, globalCfg.Runtime.MaxRecords, globalCfg.Runtime.MaxBytes)
	r.queryRunners["file.ndjson"] = stdio.NewFileSource(globalCfg.Runtime.MaxRecords, globalCfg.Runtime.MaxBytes)
	r.publishers["stdout.default"] = stdio.NewSink(stdout)
	r.registerOpenSearch(globalCfg.OpenSearch, globalCfg.Runtime.MaxRecords, globalCfg.Runtime.MaxBytes)
	r.registerPubSub(globalCfg.PubSub)
	r.registerBigQuery(globalCfg.BigQuery, globalCfg.Runtime.MaxRecords, globalCfg.Runtime.MaxBytes)
	r.registerClickHouse(globalCfg.ClickHouse)
	r.registerSlack(globalCfg.Slack)
	r.registerWebhook(globalCfg.Webhook)
	return r
}

func (r *Registry) registerClickHouse(connectors config.ClickHouseConnectors) {
	for name, value := range connectors.Instances {
		cfg := cloneClickHouseConfig(value)
		instance := "clickhouse." + name
		var once sync.Once
		var client *clickhouseconnector.Client
		var openErr error
		open := func(ctx context.Context) (*clickhouseconnector.Client, error) {
			once.Do(func() {
				resolved := cloneClickHouseConfig(cfg)
				if openErr = config.ResolveEnv(&resolved); openErr == nil {
					client, openErr = clickhouseconnector.Open(ctx, resolved)
				}
			})
			return client, openErr
		}
		validate := func(ctx context.Context) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			resolved := cloneClickHouseConfig(cfg)
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

func cloneClickHouseConfig(cfg config.ClickHouseConfig) config.ClickHouseConfig {
	cfg.Addresses = append([]string(nil), cfg.Addresses...)
	if cfg.Query != nil {
		query := *cfg.Query
		cfg.Query = &query
	}
	if cfg.Sink != nil {
		sink := *cfg.Sink
		cfg.Sink = &sink
	}
	return cfg
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
				URL: resolved.URL, Username: resolved.Username, Password: resolved.Password, Index: resolved.Index,
				InsecureSkipVerify: resolved.InsecureSkipVerify, MaxRows: maxRows,
				MaxBytes: maxBytes, SQLFetchSize: resolved.SQLFetchSize,
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

func (r *Registry) registerWebhook(connectors config.WebhookConnectors) {
	for name, value := range connectors.Instances {
		cfg := cloneWebhookConfig(value)
		instance := "webhook." + name
		factory := func(ctx context.Context) (*webhook.Client, error) {
			resolved := cloneWebhookConfig(cfg)
			if err := config.ResolveEnv(&resolved); err != nil {
				return nil, err
			}
			return webhook.New(ctx, webhook.Config{
				URL: resolved.URL, Headers: resolved.Headers, SigningSecret: resolved.SigningSecret,
				Timeout: resolved.Timeout.Value(), MaxAttempts: resolved.MaxAttempts,
				MaxFindings: resolved.MaxFindings, MaxPayloadBytes: int(resolved.MaxPayloadBytes),
			})
		}
		r.publisherValidators[instance] = func(ctx context.Context) error {
			_, err := factory(ctx)
			return err
		}
		r.publisherFactories[instance] = func(ctx context.Context) (Publisher, error) { return factory(ctx) }
	}
}

func cloneWebhookConfig(cfg config.WebhookConfig) config.WebhookConfig {
	if cfg.Headers != nil {
		headers := cfg.Headers
		cfg.Headers = make(map[string]string, len(cfg.Headers))
		for name, value := range headers {
			cfg.Headers[name] = value
		}
	}
	return cfg
}

func (r *Registry) GetQueryRunner(name string) (QueryRunner, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, errors.New("connector registry is closed")
	}
	if source, ok := r.queryRunners[name]; ok {
		r.mu.Unlock()
		return source, nil
	}
	factory, ok := r.queryFactories[name]
	if !ok {
		r.mu.Unlock()
		return nil, fmt.Errorf("query runner %q not found", name)
	}
	if pending, ok := r.queryInitializations[name]; ok {
		r.mu.Unlock()
		<-pending.done
		if pending.err != nil {
			return nil, fmt.Errorf("initialize query runner %q: %w", name, pending.err)
		}
		return pending.source, nil
	}
	pending := &queryInitialization{done: make(chan struct{})}
	r.queryInitializations[name] = pending
	r.initializing.Add(1)
	r.mu.Unlock()
	defer r.initializing.Done()

	source, err := factory(r.ctx)
	if err == nil && source == nil {
		err = errors.New("factory returned a nil source")
	}
	r.mu.Lock()
	if r.closed && err == nil {
		err = errors.New("connector registry is closed")
	} else if err == nil {
		r.queryRunners[name] = source
		r.trackCloser(source)
	}
	r.mu.Unlock()
	if err != nil {
		err = r.closeFailedInitialization("query runner", name, source, err)
	}
	r.mu.Lock()
	pending.source = source
	pending.err = err
	delete(r.queryInitializations, name)
	close(pending.done)
	r.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("initialize query runner %q: %w", name, err)
	}
	return source, nil
}

func (r *Registry) GetPublisher(name string) (Publisher, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, errors.New("connector registry is closed")
	}
	if sink, ok := r.publishers[name]; ok {
		r.mu.Unlock()
		return sink, nil
	}
	factory, ok := r.publisherFactories[name]
	if !ok {
		r.mu.Unlock()
		return nil, fmt.Errorf("publisher %q not found", name)
	}
	if pending, ok := r.publisherInitializations[name]; ok {
		r.mu.Unlock()
		<-pending.done
		if pending.err != nil {
			return nil, fmt.Errorf("initialize publisher %q: %w", name, pending.err)
		}
		return pending.sink, nil
	}
	pending := &publisherInitialization{done: make(chan struct{})}
	r.publisherInitializations[name] = pending
	r.initializing.Add(1)
	r.mu.Unlock()
	defer r.initializing.Done()

	sink, err := factory(r.ctx)
	if err == nil && sink == nil {
		err = errors.New("factory returned a nil publisher")
	}
	r.mu.Lock()
	if r.closed && err == nil {
		err = errors.New("connector registry is closed")
	} else if err == nil {
		r.publishers[name] = sink
		r.trackCloser(sink)
	}
	r.mu.Unlock()
	if err != nil {
		err = r.closeFailedInitialization("publisher", name, sink, err)
	}
	r.mu.Lock()
	pending.sink = sink
	pending.err = err
	delete(r.publisherInitializations, name)
	close(pending.done)
	r.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("initialize publisher %q: %w", name, err)
	}
	return sink, nil
}

func (r *Registry) closeFailedInitialization(role, name string, value any, initializationErr error) error {
	closer, ok := value.(Closer)
	if !ok {
		return initializationErr
	}
	if err := closer.Close(); err != nil {
		cleanupErr := fmt.Errorf("close %s %q after initialization failure: %w", role, name, err)
		r.mu.Lock()
		if r.closed {
			r.initializationCleanupErrs = append(r.initializationCleanupErrs, cleanupErr)
		}
		r.mu.Unlock()
		return errors.Join(initializationErr, cleanupErr)
	}
	return initializationErr
}

// ValidateReferences checks connector names and roles without resolving secrets.
func (r *Registry) ValidateReferences(rule *config.RuleConfig) error {
	return r.validateReferences(rule, true)
}

func (r *Registry) validateReferences(rule *config.RuleConfig, includeBestEffort bool) error {
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
	publishers := rule.Publishers
	if includeBestEffort {
		publishers = append(append([]string(nil), publishers...), rule.BestEffortPublishers...)
	}
	for _, name := range publishers {
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
	if err := r.validateReferences(rule, includeBestEffort); err != nil {
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
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		r.mu.Unlock()
		r.initializing.Wait()

		r.mu.Lock()
		closers := append([]Closer(nil), r.closers...)
		initializationCleanupErrs := append([]error(nil), r.initializationCleanupErrs...)
		r.closers = nil
		r.initializationCleanupErrs = nil
		r.mu.Unlock()

		errs := make([]error, 0, len(closers)+len(initializationCleanupErrs))
		errs = append(errs, initializationCleanupErrs...)
		for i := len(closers) - 1; i >= 0; i-- {
			if err := closers[i].Close(); err != nil {
				errs = append(errs, err)
			}
		}
		r.closeErr = errors.Join(errs...)
	})
	return r.closeErr
}
