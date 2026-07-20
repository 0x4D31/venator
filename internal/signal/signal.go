package signal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/0x4D31/venator/internal/config"
	"github.com/0x4D31/venator/internal/model"
)

// Signal is the normalized output produced by signal-format rules.
type Signal struct {
	Timestamp        time.Time      `json:"timestamp,omitzero"`
	RuleID           string         `json:"rule_id"`
	RuleName         string         `json:"rule_name"`
	ConfidenceID     int            `json:"confidence_id"`
	Confidence       string         `json:"confidence"`
	TTPs             []TTP          `json:"ttps"`
	Actor            Actor          `json:"actor"`
	Resource         Resource       `json:"resource"`
	SrcEndpoint      Endpoint       `json:"src_endpoint"`
	DstEndpoint      Endpoint       `json:"dst_endpoint"`
	Message          string         `json:"message"`
	Metadata         Metadata       `json:"metadata"`
	RuleSpecificData map[string]any `json:"rule_specific_data,omitempty"`
}

// TTP identifies a technique or tactic attached to the rule.
type TTP struct {
	Framework string `json:"framework"`
	Tactic    string `json:"tactic"`
	Name      string `json:"name"`
	ID        string `json:"id"`
	Reference string `json:"reference"`
}

type Actor struct {
	User User `json:"user"`
}

type User struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

// Resource identifies the target of the activity.
type Resource struct {
	Name string `json:"name"`
	Type string `json:"type"`
	UID  string `json:"uid"`
}

// Endpoint represents a network endpoint.
type Endpoint struct {
	Hostname string `json:"hostname"`
	IP       string `json:"ip"`
}

// Metadata contains event identifiers.
type Metadata struct {
	EventID    string `json:"event_id"`
	EventIndex string `json:"event_index"`
}

const (
	ConfidenceUnknown int = 0
	ConfidenceLow     int = 1
	ConfidenceMedium  int = 2
	ConfidenceHigh    int = 3
)

func BuildSignal(result model.Record, cfg *config.RuleConfig) (*Signal, error) {
	signal := Signal{
		RuleID:       cfg.UID,
		RuleName:     cfg.Name,
		ConfidenceID: getConfidenceID(cfg.Confidence),
		Confidence:   string(cfg.Confidence),
		TTPs:         make([]TTP, 0, len(cfg.TTPs)),
	}
	for _, ttp := range cfg.TTPs {
		signal.TTPs = append(signal.TTPs, TTP{
			Framework: ttp.Framework,
			Tactic:    ttp.Tactic,
			Name:      ttp.Name,
			ID:        ttp.ID,
			Reference: ttp.Reference,
		})
	}

	for _, outputField := range cfg.Output.Fields {
		rawValue, exists := result[outputField.Source]
		if !exists {
			return nil, fmt.Errorf("source field %s not found in query results", outputField.Source)
		}
		value, ok := model.StringValue(rawValue)
		if !ok {
			return nil, fmt.Errorf("source field %s cannot be represented as text", outputField.Source)
		}
		switch outputField.Field {
		case "Timestamp":
			if typedTime, ok := rawValue.(time.Time); ok {
				signal.Timestamp = typedTime.UTC()
				break
			}
			parsedTime, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				return nil, fmt.Errorf("failed to parse timestamp from %s: %w", outputField.Source, err)
			}
			signal.Timestamp = parsedTime.UTC()
		case "ActorUserName":
			signal.Actor.User.Name = value
		case "ActorUserUID":
			signal.Actor.User.UID = value
		case "ResourceName":
			signal.Resource.Name = value
		case "ResourceType":
			signal.Resource.Type = value
		case "ResourceUID":
			signal.Resource.UID = value
		case "SrcHostname":
			signal.SrcEndpoint.Hostname = value
		case "SrcIP":
			signal.SrcEndpoint.IP = value
		case "DstHostname":
			signal.DstEndpoint.Hostname = value
		case "DstIP":
			signal.DstEndpoint.IP = value
		case "Message":
			signal.Message = value
		case "EventID":
			signal.Metadata.EventID = value
		case "EventIndex":
			signal.Metadata.EventIndex = value
		case "RuleSpecificData":
			switch typed := rawValue.(type) {
			case map[string]any:
				signal.RuleSpecificData = typed
			default:
				if object, ok := decodeJSONObject(value); ok {
					signal.RuleSpecificData = object
				} else {
					signal.RuleSpecificData = map[string]any{"raw": rawValue}
				}
			}

		default:
			return nil, fmt.Errorf("unsupported output field %s", outputField.Field)
		}
	}
	return &signal, nil
}

func decodeJSONObject(value string) (map[string]any, bool) {
	decoder := json.NewDecoder(bytes.NewBufferString(value))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, false
	}
	return object, true
}

func BuildOutput(result model.Record, cfg *config.RuleConfig) (any, error) {
	var output any
	switch cfg.Output.Format {
	case config.OutputFormatSignal:
		sig, err := BuildSignal(result, cfg)
		if err != nil {
			return nil, err
		}
		output = sig
	case config.OutputFormatRaw:
		output = result
	default:
		return nil, fmt.Errorf("unsupported output format: %s", cfg.Output.Format)
	}
	return output, nil
}

func getConfidenceID(confidence config.ConfidenceLevel) int {
	switch confidence {
	case config.ConfidenceLow:
		return ConfidenceLow
	case config.ConfidenceMedium:
		return ConfidenceMedium
	case config.ConfidenceHigh:
		return ConfidenceHigh
	default:
		return ConfidenceUnknown
	}
}
