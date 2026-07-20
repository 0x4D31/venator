package webhook

import "time"

// Config controls delivery of canonical findings to a generic HTTP endpoint.
type Config struct {
	URL             string
	Headers         map[string]string
	SigningSecret   string
	Timeout         time.Duration
	MaxAttempts     int
	MaxFindings     int
	MaxPayloadBytes int
}
