package connector

import (
	"context"

	"github.com/0x4D31/venator/internal/config"
	"github.com/0x4D31/venator/internal/model"
)

// QueryRunner executes a rule against a configured source.
type QueryRunner interface {
	Query(ctx context.Context, ruleConfig *config.RuleConfig) ([]model.Record, error)
}

// Publisher delivers a canonical finding batch.
type Publisher interface {
	Publish(ctx context.Context, batch model.PublishBatch) error
}

// Closer releases connector resources.
type Closer interface {
	Close() error
}
