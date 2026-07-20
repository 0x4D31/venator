package pubsub

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	gcppubsub "cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"cloud.google.com/go/pubsub/v2/pstest"

	"github.com/0x4D31/venator/internal/model"
)

func TestBuildPubSubMessageUsesCanonicalFinding(t *testing.T) {
	finding := testFinding()
	message, err := buildPubSubMessage(finding)
	if err != nil {
		t.Fatalf("buildPubSubMessage() error: %v", err)
	}

	var got model.Finding
	if err := json.Unmarshal(message.Data, &got); err != nil {
		t.Fatalf("decode message data: %v", err)
	}
	if got.ID != finding.ID || got.RunID != finding.RunID || got.Rule.ID != finding.Rule.ID {
		t.Fatalf("message does not contain canonical finding: %+v", got)
	}
	for key, want := range map[string]string{
		"schema_version": finding.SchemaVersion,
		"finding_id":     finding.ID,
		"run_id":         finding.RunID,
		"rule_id":        finding.Rule.ID,
	} {
		if got := message.Attributes[key]; got != want {
			t.Errorf("attribute %q = %q, want %q", key, got, want)
		}
	}
}

func TestPublishReturnsSerializationErrorBeforeNetwork(t *testing.T) {
	client, err := New(context.Background(), Config{ProjectID: "not-a-real-project", TopicID: "not-a-real-topic"})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	finding := testFinding()
	finding.Payload = make(chan int)

	err = client.Publish(context.Background(), model.PublishBatch{Findings: []model.Finding{finding}})
	if err == nil {
		t.Fatal("Publish() error = nil, want a serialization error")
	}
	for _, want := range []string{"encode Pub/Sub finding", finding.ID, "unsupported type"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Publish() error = %q, want substring %q", err, want)
		}
	}
}

func TestBuildPubSubMessageRejectsServiceLimitBeforeNetwork(t *testing.T) {
	finding := testFinding()
	finding.Payload = strings.Repeat("x", maxPubSubMessageBytes)
	message, err := buildPubSubMessage(finding)
	if err != nil {
		t.Fatal(err)
	}
	err = validatePubSubMessageSize("test-project", "test-topic", message)
	if err == nil || !strings.Contains(err.Error(), "service limit") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidatePubSubMessageSizeExactRequestBoundary(t *testing.T) {
	message := &gcppubsub.Message{Attributes: map[string]string{"key": "value"}}
	low, high := 0, maxPubSubMessageBytes
	for low < high {
		mid := low + (high-low+1)/2
		message.Data = make([]byte, mid)
		if validatePubSubMessageSize("project", "topic", message) == nil {
			low = mid
		} else {
			high = mid - 1
		}
	}
	message.Data = make([]byte, low)
	if err := validatePubSubMessageSize("project", "topic", message); err != nil {
		t.Fatalf("largest valid request rejected: %v", err)
	}
	message.Data = append(message.Data, 0)
	if err := validatePubSubMessageSize("project", "topic", message); err == nil {
		t.Fatal("first request above the service boundary was accepted")
	}
}

func TestPublishEmptyBatchDoesNotRequireNetwork(t *testing.T) {
	client, err := New(context.Background(), Config{ProjectID: "not-a-real-project", TopicID: "not-a-real-topic"})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if err := client.Publish(context.Background(), model.PublishBatch{}); err != nil {
		t.Fatalf("Publish() error: %v", err)
	}
}

func TestPublishRoundTripsThroughEmulator(t *testing.T) {
	server := pstest.NewServer()
	t.Cleanup(func() {
		server.Close()
		server.Wait()
	})
	t.Setenv("PUBSUB_EMULATOR_HOST", server.Addr)
	const projectID = "venator-test"
	const topicID = "findings"
	topicName := "projects/" + projectID + "/topics/" + topicID
	if _, err := server.GServer.CreateTopic(context.Background(), &pubsubpb.Topic{Name: topicName}); err != nil {
		t.Fatal(err)
	}

	client, err := New(context.Background(), Config{ProjectID: projectID, TopicID: topicID})
	if err != nil {
		t.Fatal(err)
	}
	finding := testFinding()
	if err := client.Publish(context.Background(), model.PublishBatch{Findings: []model.Finding{finding}}); err != nil {
		t.Fatal(err)
	}
	messages := server.Messages()
	if len(messages) != 1 {
		t.Fatalf("published messages = %d, want 1", len(messages))
	}
	if messages[0].Topic != topicName || messages[0].Attributes["finding_id"] != finding.ID {
		t.Fatalf("unexpected published message: %+v", messages[0])
	}
	var got model.Finding
	if err := json.Unmarshal(messages[0].Data, &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != finding.ID || got.Rule.ID != finding.Rule.ID {
		t.Fatalf("finding = %+v", got)
	}
}

func TestNewValidatesConfig(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  Config
		want string
	}{
		{name: "missing project", cfg: Config{TopicID: "topic"}, want: "project ID is required"},
		{name: "missing topic", cfg: Config{ProjectID: "project"}, want: "topic ID is required"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(context.Background(), tt.cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("New() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func testFinding() model.Finding {
	return model.Finding{
		SchemaVersion: model.FindingSchemaVersion,
		ID:            "finding-1",
		RunID:         "run-1",
		DetectedAt:    time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC),
		Source:        "opensearch.logs",
		OutputFormat:  "signal",
		Rule: model.RuleMetadata{
			ID:         "rule-1",
			Name:       "Test rule",
			Confidence: "high",
		},
		Payload: map[string]any{"event": "login", "success": false},
	}
}
