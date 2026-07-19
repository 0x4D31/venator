package model

import "context"

type Request struct {
	System string
	User   string
}

// Client defines the interface for LLM clients
type Client interface {
	Call(ctx context.Context, request Request) (string, error)
}
