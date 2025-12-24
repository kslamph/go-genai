package provider

import (
	"context"

	"github.com/sashabaranov/go-openai"
)

// Provider represents a generic LLM provider
type Provider interface {
	// Type returns the provider identifier (e.g., "gemini", "kiro")
	Type() string

	// Name returns the unique name/ID of this specific instance/credential
	Name() string

	// ChatCompletion handles a single chat request
	ChatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (*openai.ChatCompletionResponse, error)

	// StreamChatCompletion handles a streaming chat request
	// It returns a channel that emits chunks.
	StreamChatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (<-chan openai.ChatCompletionStreamResponse, <-chan error)

	// ListModels returns a list of models supported by the provider
	ListModels(ctx context.Context) ([]string, error)
}
