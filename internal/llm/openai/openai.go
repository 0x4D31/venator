package openai

import (
	"context"
	"fmt"
	"net/url"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"

	llmconfig "github.com/0x4D31/venator/internal/llm/config"
	"github.com/0x4D31/venator/internal/llm/model"
)

type Client struct {
	client      openai.Client
	model       string
	temperature float64
}

func New(cfg llmconfig.Config) (model.Client, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("OpenAI API key is required")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("OpenAI model is required")
	}
	options := []option.RequestOption{option.WithAPIKey(cfg.APIKey)}
	if cfg.ServerURL != "" {
		parsed, err := url.Parse(cfg.ServerURL)
		if err != nil || parsed.Host == "" || parsed.User != nil {
			return nil, fmt.Errorf("OpenAI server URL is invalid")
		}
		if parsed.Scheme != "https" && !(parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1")) {
			return nil, fmt.Errorf("OpenAI server URL must use HTTPS (HTTP is allowed only on loopback)")
		}
		options = append(options, option.WithBaseURL(cfg.ServerURL))
	}
	return &Client{client: openai.NewClient(options...), model: cfg.Model, temperature: cfg.Temperature}, nil
}

func (c *Client) Call(ctx context.Context, request model.Request) (string, error) {
	format := responses.ResponseFormatTextConfigParamOfJSONSchema("venator_review", reviewSchema)
	format.OfJSONSchema.Strict = openai.Bool(true)
	params := responses.ResponseNewParams{
		Model:           c.model,
		Instructions:    openai.String(request.System),
		Input:           responses.ResponseNewParamsInputUnion{OfString: openai.String(request.User)},
		Store:           openai.Bool(false),
		MaxOutputTokens: openai.Int(4096),
		Text: responses.ResponseTextConfigParam{
			Format: format,
		},
		Temperature: openai.Float(c.temperature),
	}
	response, err := c.client.Responses.New(ctx, params)
	if err != nil {
		return "", fmt.Errorf("create OpenAI response: %w", err)
	}
	output := response.OutputText()
	if output == "" {
		return "", fmt.Errorf("OpenAI response contained no output text")
	}
	return output, nil
}

var reviewSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"decisions"},
	"properties": map[string]any{
		"decisions": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"finding_id", "verdict", "reason"},
				"properties": map[string]any{
					"finding_id": map[string]any{"type": "string"},
					"verdict":    map[string]any{"type": "string", "enum": []string{"suspicious", "benign", "uncertain"}},
					"reason":     map[string]any{"type": "string"},
				},
			},
		},
	},
}
