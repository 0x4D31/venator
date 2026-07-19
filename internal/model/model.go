// Package model contains the scheduler- and connector-neutral data contracts.
package model

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

const (
	FindingSchemaVersion = "venator.finding/v1"
	RunSchemaVersion     = "venator.run/v1"
)

// Record preserves the source system's native JSON-compatible value types.
type Record map[string]any

type RuleMetadata struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Status     string   `json:"status,omitempty"`
	Confidence string   `json:"confidence"`
	Tags       []string `json:"tags,omitempty"`
	TTPIDs     []string `json:"ttp_ids,omitempty"`
}

// FindingAttributes contains commonly queried fields. Payload remains the
// lossless source of truth, while sinks such as ClickHouse can index these.
type FindingAttributes struct {
	ActorUserName string `json:"actor_user_name,omitempty"`
	ActorUserUID  string `json:"actor_user_uid,omitempty"`
	ResourceName  string `json:"resource_name,omitempty"`
	ResourceType  string `json:"resource_type,omitempty"`
	ResourceUID   string `json:"resource_uid,omitempty"`
	SrcHostname   string `json:"src_hostname,omitempty"`
	SrcIP         string `json:"src_ip,omitempty"`
	DstHostname   string `json:"dst_hostname,omitempty"`
	DstIP         string `json:"dst_ip,omitempty"`
	Message       string `json:"message,omitempty"`
	EventID       string `json:"event_id,omitempty"`
	EventIndex    string `json:"event_index,omitempty"`
}

// Review is advisory enrichment. It never replaces or deletes a deterministic
// finding.
type Review struct {
	Verdict    string `json:"verdict"`
	Reason     string `json:"reason"`
	Reviewer   string `json:"reviewer"`
	ReviewedAt string `json:"reviewed_at"`
}

// Finding is the single canonical object delivered to every sink.
type Finding struct {
	SchemaVersion string            `json:"schema_version"`
	ID            string            `json:"id"`
	RunID         string            `json:"run_id"`
	DetectedAt    time.Time         `json:"detected_at"`
	EventAt       *time.Time        `json:"event_at,omitempty"`
	Source        string            `json:"source"`
	OutputFormat  string            `json:"output_format"`
	Rule          RuleMetadata      `json:"rule"`
	Attributes    FindingAttributes `json:"attributes,omitempty"`
	Payload       any               `json:"payload"`
	Review        *Review           `json:"review,omitempty"`
}

type PublishBatch struct {
	RunID      string       `json:"run_id"`
	DetectedAt time.Time    `json:"detected_at"`
	Source     string       `json:"source"`
	Rule       RuleMetadata `json:"rule"`
	Findings   []Finding    `json:"findings"`
}

type SinkReceipt struct {
	Name       string `json:"name"`
	Required   bool   `json:"required"`
	Attempted  int    `json:"attempted"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

type RunReport struct {
	SchemaVersion string        `json:"schema_version"`
	RunID         string        `json:"run_id"`
	RuleID        string        `json:"rule_id"`
	RuleName      string        `json:"rule_name"`
	Status        string        `json:"status"`
	StartedAt     time.Time     `json:"started_at"`
	FinishedAt    time.Time     `json:"finished_at"`
	Queried       int           `json:"queried"`
	Excluded      int           `json:"excluded"`
	Findings      int           `json:"findings"`
	ReviewError   string        `json:"review_error,omitempty"`
	Sinks         []SinkReceipt `json:"sinks,omitempty"`
	Error         string        `json:"error,omitempty"`
}

func NewRunID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate run ID: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// FindingID is stable across scheduler retries for the same rule and evidence.
func FindingID(ruleID string, evidence Record) (string, error) {
	return FindingIDForOccurrence(ruleID, evidence, 0)
}

// FindingIDForOccurrence keeps byte-identical rows distinct without relying on
// run IDs. The zero-based occurrence is counted independently for each unique
// evidence object, so retrying the same result set produces the same IDs.
func FindingIDForOccurrence(ruleID string, evidence Record, occurrence int) (string, error) {
	if occurrence < 0 {
		return "", fmt.Errorf("finding occurrence cannot be negative")
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return "", fmt.Errorf("encode evidence for finding ID: %w", err)
	}
	h := sha256.New()
	_, _ = h.Write([]byte(ruleID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(encoded)
	if occurrence > 0 {
		_, _ = fmt.Fprintf(h, "\x00%d", occurrence)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// StringValue converts a typed source value only at a textual output boundary.
func StringValue(value any) (string, bool) {
	if value == nil {
		return "", true
	}
	switch v := value.(type) {
	case string:
		return v, true
	case []byte:
		return string(v), true
	case time.Time:
		return v.UTC().Format(time.RFC3339Nano), true
	case *time.Time:
		if v == nil {
			return "", true
		}
		return v.UTC().Format(time.RFC3339Nano), true
	case json.Number:
		return v.String(), true
	}
	if encoded, err := json.Marshal(value); err == nil {
		return string(encoded), true
	}
	return "", false
}
