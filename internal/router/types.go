package router

import (
	"context"
	"io"
	"net/http"

	"github.com/sunbankio/omniproxy/internal/provider"
)

// Request represents a unified request to be routed to the appropriate provider
type Request struct {
	Protocol provider.Protocol // The API protocol (OpenAI or Gemini)
	Model    string            // The model name to use
	Payload  interface{}       // The request payload (flexible for different protocols)
	Headers  map[string]string // Additional headers to pass to the provider
	IsStream bool              // Whether this is a streaming request
}

// Response represents a unified response from a provider
type Response struct {
	StatusCode int               // HTTP status code
	Body       io.ReadCloser     // Response body (can be a stream for streaming responses)
	Headers    map[string]string // Response headers
}

// Router defines the interface for routing requests to appropriate providers
type Router interface {
	// Execute routes a request to the appropriate provider and returns the response
	// This is the main method that handles both streaming and non-streaming requests
	Execute(ctx context.Context, req *Request) (*Response, error)

	// HandleOpenAIRequest handles OpenAI-compatible API requests
	// This method parses the HTTP request, creates a Request struct, and writes the response
	HandleOpenAIRequest(w http.ResponseWriter, r *http.Request)

	// HandleGeminiRequest handles Gemini API requests
	// This method parses the HTTP request, creates a Request struct, and writes the response
	HandleGeminiRequest(w http.ResponseWriter, r *http.Request)
}

// CredentialErrorRecorder defines the interface for recording errors on credentials
// This allows the router to update credential state without creating an import cycle
type CredentialErrorRecorder interface {
	// RecordError records an error for a credential and updates its state based on error type
	// For 429 errors, it sets AvailableAt or increases FailureCount for exponential backoff
	RecordError(credentialID string, model string, err error)

	// RecordSuccess records a successful request for a credential
	// Resets FailureCount to 0 to prevent backoff accumulation across unrelated failures
	RecordSuccess(credentialID string, model string)

	// MarkDead marks a credential as permanently dead (e.g., when refresh token is revoked)
	MarkDead(credentialID string, model string)
}

// CredentialSelector defines the interface for selecting credentials
// This allows the router to get credentials without creating an import cycle
type CredentialSelector interface {
	// GetCredential returns a credential for the specified model
	// Returns nil if no credential is available
	GetCredential(model string) interface{}
}
