// Package provider defines the contract implemented by LLM review providers.
package provider

import "context"

type Provider string

const OpenAI Provider = "openai"

type Config struct {
	Provider    Provider
	APIKey      string
	Model       string
	Temperature *float64
	ServerURL   string
}

type Request struct {
	System string
	User   string
}

// Client submits a review request to an LLM provider.
type Client interface {
	Call(ctx context.Context, request Request) (string, error)
}
