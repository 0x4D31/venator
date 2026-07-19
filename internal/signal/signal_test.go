package signal_test

import (
	"strings"
	"testing"
	"time"

	"github.com/0x4D31/venator/internal/config"
	"github.com/0x4D31/venator/internal/model"
	"github.com/0x4D31/venator/internal/signal"
	"github.com/google/go-cmp/cmp"
)

func TestBuildSignal(t *testing.T) {
	cfg := &config.RuleConfig{
		Name: "test-rule",
		UID:  "test-uid",
		Output: config.Output{
			Fields: []config.OutputField{
				{Field: "Timestamp", Source: "timestamp"},
				{Field: "Message", Source: "message"},
				{Field: "ResourceName", Source: "hostname"},
				{Field: "ActorUserName", Source: "actor.user.name"},
				{Field: "SrcHostname", Source: "src.hostname"},
				{Field: "SrcIP", Source: "src.ip"},
				{Field: "RuleSpecificData", Source: "rule_specific_data"},
			},
		},
	}
	// config with unsupported output field
	cfgInvalidOutput := &config.RuleConfig{
		Name: "test-rule",
		UID:  "test-uid",
		Output: config.Output{
			Fields: []config.OutputField{
				{Field: "Timestamp", Source: "timestamp"},
				{Field: "Message", Source: "message"},
				{Field: "ResourceName", Source: "device.hostname"},
				{Field: "UnsupportedField", Source: "unsupported"},
			},
		},
	}

	tests := []struct {
		name       string
		result     model.Record
		cfg        *config.RuleConfig
		errMessage string
		expected   *signal.Signal
	}{
		{
			name: "successful mapping",
			result: model.Record{
				"timestamp":          "2023-05-14T10:00:00Z",
				"message":            "process xyz created/modified the file abc",
				"hostname":           "hostname123",
				"actor.user.name":    "user123",
				"src.hostname":       "src-hostname",
				"src.ip":             "src-ip",
				"rule_specific_data": `{"key1": "value1", "key2": "value2"}`,
			},
			cfg:        cfg,
			errMessage: "",
			expected: &signal.Signal{
				Timestamp: time.Date(2023, 5, 14, 10, 0, 0, 0, time.UTC),
				RuleID:    cfg.UID,
				RuleName:  cfg.Name,
				TTPs:      []map[string]string{},
				Message:   "process xyz created/modified the file abc",
				Resource:  signal.Resource{Name: "hostname123", Type: "", UID: ""},
				Actor: signal.Actor{
					User: signal.User{Name: "user123", UID: ""},
				},
				SrcEndpoint: signal.Endpoint{Hostname: "src-hostname", IP: "src-ip"},
				DstEndpoint: signal.Endpoint{Hostname: "", IP: ""},
				RuleSpecificData: map[string]any{
					"key1": "value1",
					"key2": "value2",
				},
			},
		},
		{
			name: "successful mapping with invalid RuleSpecificData format",
			result: model.Record{
				"timestamp":          "2023-05-14T10:00:00Z",
				"message":            "process xyz created/modified the file abc",
				"hostname":           "hostname123",
				"actor.user.name":    "user123",
				"src.hostname":       "src-hostname",
				"src.ip":             "src-ip",
				"rule_specific_data": `key1: value1, key2: value2`, // invalid JSON
			},
			cfg:        cfg,
			errMessage: "",
			expected: &signal.Signal{
				Timestamp: time.Date(2023, 5, 14, 10, 0, 0, 0, time.UTC),
				RuleID:    cfg.UID,
				RuleName:  cfg.Name,
				TTPs:      []map[string]string{},
				Message:   "process xyz created/modified the file abc",
				Resource:  signal.Resource{Name: "hostname123", Type: "", UID: ""},
				Actor: signal.Actor{
					User: signal.User{Name: "user123", UID: ""},
				},
				SrcEndpoint: signal.Endpoint{Hostname: "src-hostname", IP: "src-ip"},
				DstEndpoint: signal.Endpoint{Hostname: "", IP: ""},
				RuleSpecificData: map[string]any{
					"raw": "key1: value1, key2: value2",
				},
			},
		},
		{
			name: "missing field in result",
			result: model.Record{
				"timestamp":          "2023-05-14T10:00:00Z",
				"raw":                "process xyz created/modified the file abc",
				"device.hostname":    "hostname123",
				"actor.user.name":    "user123",
				"src.hostname":       "src-hostname",
				"src.ip":             "src-ip",
				"rule_specific_data": "",
			},
			cfg:        cfg,
			errMessage: "source field message not found in query results",
			expected:   nil,
		},
		{
			name: "invalid timestamp format",
			result: model.Record{
				"timestamp":          "invalid-timestamp",
				"message":            "process xyz created/modified the file abc",
				"hostname":           "hostname123",
				"actor.user.name":    "user123",
				"src.hostname":       "src-hostname",
				"src.ip":             "src-ip",
				"rule_specific_data": "",
			},
			cfg:        cfg,
			errMessage: "parsing time",
			expected:   nil,
		},
		{
			name: "unsupported output field",
			result: model.Record{
				"timestamp":       "2023-05-14T10:00:00Z",
				"message":         "process xyz created/modified the file abc",
				"device.hostname": "hostname123",
				"unsupported":     "unsupported value",
			},
			cfg:        cfgInvalidOutput,
			errMessage: "unsupported output field UnsupportedField",
			expected:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.RuleConfig{}
			*cfg = *tt.cfg
			signal, err := signal.BuildSignal(tt.result, cfg)
			if err != nil {
				if tt.expected != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !strings.Contains(err.Error(), tt.errMessage) {
					t.Fatalf("error message does not contain expected substring: %s", tt.errMessage)
				}
				return
			}
			if diff := cmp.Diff(tt.expected, signal); diff != "" {
				t.Fatalf("unexpected result (-want +got):\n%s", diff)
			}
		})
	}
}

func TestBuildSignalAllowsOneSourceForMultipleFieldsAndTypedTime(t *testing.T) {
	timestamp := time.Date(2026, 7, 18, 12, 0, 0, 123, time.FixedZone("offset", 3600))
	cfg := &config.RuleConfig{Name: "test", UID: "id", Output: config.Output{Fields: []config.OutputField{
		{Field: "Timestamp", Source: "time"},
		{Field: "ResourceName", Source: "host"},
		{Field: "DstHostname", Source: "host"},
	}}}
	got, err := signal.BuildSignal(model.Record{"time": timestamp, "host": "server-1"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got.Resource.Name != "server-1" || got.DstEndpoint.Hostname != "server-1" {
		t.Fatalf("signal = %#v", got)
	}
	if !got.Timestamp.Equal(timestamp) || got.Timestamp.Location() != time.UTC {
		t.Fatalf("timestamp = %v", got.Timestamp)
	}
}

func TestBuildSignalPreservesTypedRuleSpecificScalar(t *testing.T) {
	cfg := &config.RuleConfig{Name: "test", UID: "id", Output: config.Output{Fields: []config.OutputField{
		{Field: "RuleSpecificData", Source: "failures"},
	}}}
	got, err := signal.BuildSignal(model.Record{"failures": int64(12)}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := got.RuleSpecificData["raw"].(int64); !ok || value != 12 {
		t.Fatalf("rule-specific data = %#v", got.RuleSpecificData)
	}
}
