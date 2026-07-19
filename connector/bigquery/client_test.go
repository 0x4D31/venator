package bigquery

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/0x4D31/venator/internal/model"
)

func TestPublishWithoutInserterReturnsErrorInsteadOfPanicking(t *testing.T) {
	client := &Client{}
	finding := model.Finding{
		ID: strings.Repeat("a", 64), RunID: "run", DetectedAt: time.Now().UTC(),
		Rule: model.RuleMetadata{ID: "rule"}, Payload: model.Record{"value": 1},
	}
	err := client.Publish(context.Background(), model.PublishBatch{Findings: []model.Finding{finding}})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("error = %v", err)
	}
}

func TestBuildRowsUsesStableFindingIDForBigQueryDedupe(t *testing.T) {
	finding := model.Finding{
		SchemaVersion: model.FindingSchemaVersion,
		ID:            strings.Repeat("b", 64), RunID: "run", DetectedAt: time.Now().UTC(),
		Rule: model.RuleMetadata{ID: "rule"}, Payload: model.Record{"value": 1},
	}
	rows, err := buildRows(context.Background(), model.PublishBatch{Findings: []model.Finding{finding}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].InsertID != finding.ID {
		t.Fatalf("rows = %#v", rows)
	}
}
