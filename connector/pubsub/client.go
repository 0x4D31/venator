package pubsub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"google.golang.org/protobuf/proto"

	"github.com/0x4D31/venator/internal/model"
)

// Google Cloud Pub/Sub limits a single message, including attributes, to
// 10,000,000 bytes. Preflight every finding before any Publish call so a large
// later finding cannot cause partial delivery of an otherwise valid batch.
const maxPubSubMessageBytes = 10_000_000

type Client struct {
	projectID string
	topicID   string
}

func New(ctx context.Context, cfg Config) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("create Pub/Sub connector: %w", err)
	}
	if cfg.ProjectID == "" {
		return nil, errors.New("create Pub/Sub connector: project ID is required")
	}
	if cfg.TopicID == "" {
		return nil, errors.New("create Pub/Sub connector: topic ID is required")
	}
	return &Client{
		projectID: cfg.ProjectID,
		topicID:   cfg.TopicID,
	}, nil
}

func (c *Client) Publish(ctx context.Context, batch model.PublishBatch) (returnErr error) {
	if len(batch.Findings) == 0 {
		return nil
	}

	// Encode every message before making any network calls. A finding that is
	// not JSON-compatible must fail the whole required sink instead of being
	// silently dropped after earlier findings have already been sent.
	messages := make([]*pubsub.Message, 0, len(batch.Findings))
	for i, finding := range batch.Findings {
		if err := ctx.Err(); err != nil {
			return err
		}
		message, err := buildPubSubMessage(finding)
		if err != nil {
			return fmt.Errorf("encode Pub/Sub finding %d (%q): %w", i, finding.ID, err)
		}
		if err := validatePubSubMessageSize(c.projectID, c.topicID, message); err != nil {
			return fmt.Errorf("encode Pub/Sub finding %d (%q): %w", i, finding.ID, err)
		}
		messages = append(messages, message)
	}

	client, err := pubsub.NewClient(ctx, c.projectID)
	if err != nil {
		return fmt.Errorf("create Pub/Sub client: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, client.Close())
	}()

	publisher := client.Publisher(c.topicID)
	defer publisher.Stop()

	publishResults := make([]*pubsub.PublishResult, 0, len(messages))
	for _, message := range messages {
		if err := ctx.Err(); err != nil {
			return err
		}
		publishResults = append(publishResults, publisher.Publish(ctx, message))
	}

	var publishErrors []error
	for i, result := range publishResults {
		if err := ctx.Err(); err != nil {
			return err
		}
		messageID, err := result.Get(ctx)
		if err != nil {
			publishErrors = append(publishErrors, fmt.Errorf("finding %q: %w", batch.Findings[i].ID, err))
			continue
		}
		if messageID == "" {
			publishErrors = append(publishErrors, fmt.Errorf("finding %q: Pub/Sub returned an empty message ID", batch.Findings[i].ID))
		}
	}
	if len(publishErrors) > 0 {
		return fmt.Errorf("publish to Pub/Sub topic %q: %w", c.topicID, errors.Join(publishErrors...))
	}

	return nil
}

func buildPubSubMessage(finding model.Finding) (*pubsub.Message, error) {
	data, err := json.Marshal(finding)
	if err != nil {
		return nil, fmt.Errorf("marshal canonical finding: %w", err)
	}

	message := &pubsub.Message{
		Data: data,
		Attributes: map[string]string{
			"schema_version": finding.SchemaVersion,
			"finding_id":     finding.ID,
			"run_id":         finding.RunID,
			"rule_id":        finding.Rule.ID,
		},
	}
	return message, nil
}

// validatePubSubMessageSize measures the exact protobuf request that would
// carry one message. The client library handles multi-message bundle limits;
// this preflight prevents an individually oversized later finding from causing
// partial delivery after earlier findings have already been queued.
func validatePubSubMessageSize(projectID, topicID string, message *pubsub.Message) error {
	topic := fmt.Sprintf("projects/%s/topics/%s", projectID, topicID)
	requestBytes := proto.Size(&pubsubpb.PublishRequest{
		Topic: topic,
		Messages: []*pubsubpb.PubsubMessage{{
			Data: message.Data, Attributes: message.Attributes, OrderingKey: message.OrderingKey,
		}},
	})
	if requestBytes > maxPubSubMessageBytes {
		return fmt.Errorf("canonical finding requires a %d-byte Pub/Sub publish request; service limit is %d", requestBytes, maxPubSubMessageBytes)
	}
	return nil
}
