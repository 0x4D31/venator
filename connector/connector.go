package connector

import (
	"context"

	"github.com/0x4D31/venator/internal/config"
	"github.com/0x4D31/venator/internal/model"
)

type Connector interface {
	New(ctx context.Context, config any) (Connector, error)
}

type QueryRunner interface {
	Query(ctx context.Context, ruleConfig *config.RuleConfig) ([]model.Record, error)
}

type Publisher interface {
	Publish(ctx context.Context, batch model.PublishBatch) error
}

type Closer interface {
	Close() error
}
