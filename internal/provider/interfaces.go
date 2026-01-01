package provider

import (
	"context"
	"fmt"

	"github.com/sashabaranov/go-openai"
	"google.golang.org/genai"
)

// ProviderError represents an error from a provider with HTTP status code information
type ProviderError struct {
	StatusCode int         // HTTP status code (e.g., 429, 404, 500)
	Message    string      // Error message
	Details    interface{} // Additional error details (optional)
	Provider   string      // Provider name that generated the error
	RetryAfter string      // Retry-After header value for rate limit errors (optional)
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

// NewProviderErrorWithRetry creates a new ProviderError with retry information
func NewProviderErrorWithRetry(statusCode int, message string, provider string, details interface{}, retryAfter string) *ProviderError {
	return &ProviderError{
		StatusCode: statusCode,
		Message:    message,
		Details:    details,
		Provider:   provider,
		RetryAfter: retryAfter,
	}
}

// Protocol represents the API protocol a provider supports
type Protocol string

const (
	ProtocolOpenAI Protocol = "openai"
	ProtocolGemini Protocol = "gemini"
)

// BaseProvider is the common interface for all providers
type BaseProvider interface {
	// Type returns the provider identifier (e.g., "gemini", "qwen", "iflow")
	Type() ProviderType

	// Name returns the unique name/ID of this specific instance/credential
	Name() string

	// SupportedProtocols returns the list of protocols this provider supports
	SupportedProtocols() []Protocol

	// ListModels returns a list of models supported by the provider
	ListModels(ctx context.Context) ([]string, error)

	// SupportsModel checks if the provider supports the given model
	SupportsModel(model string) bool
}

// OpenAICompatibleProvider is for providers with OpenAI-compatible API (qwen, iflow)
type OpenAICompatibleProvider interface {
	BaseProvider

	// ChatCompletion handles a single chat request using OpenAI format
	// Returns interface{} to allow providers to return custom response types
	ChatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (interface{}, error)

	// StreamChatCompletion handles a streaming chat request using OpenAI format
	StreamChatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (<-chan openai.ChatCompletionStreamResponse, <-chan error, error)
}

// GeminiNativeProvider is for providers using the genai package (gemini, antigravity)
type GeminiNativeProvider interface {
	BaseProvider

	// GetClient returns the underlying genai.Client for native protocol access
	GetClient() *genai.Client

	// RefreshClient recreates the genai.Client with fresh credentials after token refresh
	// This is necessary because genai.Client caches tokens internally and doesn't automatically pick up refreshed tokens
	RefreshClient(ctx context.Context) (*genai.Client, error)
}
