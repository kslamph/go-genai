package provider

import (
	"context"
	"fmt"

	"github.com/sashabaranov/go-openai"
)

// ProviderError represents an error from a provider with HTTP status code information
type ProviderError struct {
	StatusCode int         // HTTP status code (e.g., 429, 404, 500)
	Message    string      // Error message
	Details    interface{} // Additional error details (optional)
	Provider   string      // Provider name that generated the error
}

func (e *ProviderError) Error() string {
	if e.Details != nil {
		return fmt.Sprintf("[%s] %s (status: %d, details: %v)", e.Provider, e.Message, e.StatusCode, e.Details)
	}
	return fmt.Sprintf("[%s] %s (status: %d)", e.Provider, e.Message, e.StatusCode)
}

// NewProviderError creates a new ProviderError
func NewProviderError(statusCode int, message string, provider string, details interface{}) *ProviderError {
	return &ProviderError{
		StatusCode: statusCode,
		Message:    message,
		Details:    details,
		Provider:   provider,
	}
}

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

	// SupportsModel checks if the provider supports the given model
	SupportsModel(model string) bool
}
