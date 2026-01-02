package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
	"github.com/sunbankio/omniproxy/internal/auth"
	"github.com/sunbankio/omniproxy/internal/manager"
	"github.com/sunbankio/omniproxy/internal/provider"
	"github.com/sunbankio/omniproxy/pkg/utils"
	"google.golang.org/genai"
)

// formatDuration formats a time.Duration into a human-readable string
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.0f seconds", d.Seconds())
	} else if d < time.Hour {
		return fmt.Sprintf("%.0f minutes", d.Minutes())
	} else if d < 24*time.Hour {
		return fmt.Sprintf("%.1f hours", d.Hours())
	} else {
		return fmt.Sprintf("%.1f days", d.Hours()/24)
	}
}

// SmartRouterV2 handles routing with "Fast Fail" logic
type SmartRouterV2 struct {
	authManager   auth.AuthManager
	selector      CredentialSelector
	errorRecorder CredentialErrorRecorder
}

// NewSmartRouterV2 creates a new SmartRouterV2
func NewSmartRouterV2(authManager auth.AuthManager, selector CredentialSelector, errorRecorder CredentialErrorRecorder) *SmartRouterV2 {
	return &SmartRouterV2{
		authManager:   authManager,
		selector:      selector,
		errorRecorder: errorRecorder,
	}
}

// Execute handles the request execution flow
func (r *SmartRouterV2) Execute(ctx context.Context, req *Request) (*Response, error) {
	// 1. Select Credential
	cred, err := r.selectCredential(req)
	if err != nil {
		// If it's already a ProviderError, return it as-is to preserve the status code
		if pErr, ok := err.(*provider.ProviderError); ok {
			return nil, pErr
		}
		// Otherwise, wrap it in a 503 error
		return nil, &provider.ProviderError{
			StatusCode: http.StatusServiceUnavailable,
			Message:    fmt.Sprintf("No credential available: %v", err),
		}
	}

	// Ensure credential is not nil
	if cred == nil {
		return nil, &provider.ProviderError{
			StatusCode: http.StatusServiceUnavailable,
			Message:    "No credential available for this model (all credentials may be in rate limit penalty)",
		}
	}

	// 2. Proactive Auth Check (Lazy)
	if err := r.authManager.EnsureValidToken(ctx, cred); err != nil {
		utils.L().Warnf("Auth check failed for %s: %v", cred.ID, err)
		return nil, &provider.ProviderError{
			StatusCode: http.StatusUnauthorized,
			Message:    fmt.Sprintf("Authentication failed: %v", err),
			Provider:   string(cred.Type()),
		}
	}

	// 3. Inject Client (The "Dumb Provider" Part)
	if cred.ProviderType == auth.ProviderTypeGemini || cred.ProviderType == auth.ProviderTypeAntigravity {
		if cred.GetClient() == nil {
			utils.L().Infof("Initializing client for %s", cred.ID)
			if err := r.authManager.RefreshClient(ctx, cred); err != nil {
				return nil, &provider.ProviderError{
					StatusCode: http.StatusInternalServerError,
					Message:    fmt.Sprintf("Failed to initialize client: %v", err),
					Provider:   string(cred.Type()),
				}
			}
		}
	}

	// 4. Execute Request
	resp, err := r.executeWithCredential(ctx, cred, req)

	// Check retry count to prevent infinite loops
	const maxRetries = 1
	if req.RetryCount >= maxRetries {
		// Already retried once, don't retry again
		return resp, err
	}

	if err != nil {
		// 5. Error Handling (Reactive)
		if providerErr, ok := err.(*provider.ProviderError); ok {
			// If 401, attempt to refresh token and retry the request
			if providerErr.StatusCode == http.StatusUnauthorized {
				utils.L().Warnw("401 Unauthorized for credential, attempting token refresh",
					"credential_id", cred.ID,
					"provider", string(cred.Type()),
					"error", err)

				refreshErr := r.authManager.ForceRefresh(ctx, cred)
				if refreshErr != nil {
					// If refresh fails with 401, the credential is permanently revoked
					if strings.Contains(strings.ToLower(refreshErr.Error()), "401") ||
						strings.Contains(strings.ToLower(refreshErr.Error()), "unauthorized") {
						utils.L().Errorw("Credential marked as DEAD due to refresh failure (Lazarus Protocol)",
							"credential_id", cred.ID,
							"provider", string(cred.Type()),
							"error", refreshErr)
						if r.errorRecorder != nil {
							r.errorRecorder.MarkDead(cred.ID, req.Model)
						}
					}
					return nil, refreshErr
				}

				// Token refresh succeeded, now refresh the client for Gemini/Antigravity
				if cred.ProviderType == auth.ProviderTypeGemini || cred.ProviderType == auth.ProviderTypeAntigravity {
					utils.L().Infow("Token refreshed successfully, refreshing client",
						"credential_id", cred.ID,
						"provider", string(cred.Type()))
					if clientErr := r.authManager.RefreshClient(ctx, cred); clientErr != nil {
						utils.L().Errorw("Failed to refresh client after token refresh",
							"credential_id", cred.ID,
							"provider", string(cred.Type()),
							"error", clientErr)
						return nil, clientErr
					}
				}

				// Increment retry count and retry the request with refreshed token
				req.RetryCount++
				utils.L().Infow("Retrying request with refreshed token",
					"credential_id", cred.ID,
					"provider", string(cred.Type()),
					"model", req.Model,
					"retry_count", req.RetryCount)
				return r.executeWithCredential(ctx, cred, req)
			}

			// If 429, update credential state for rate limiting
			if providerErr.StatusCode == http.StatusTooManyRequests {
				utils.L().Warnw("429 Rate limit error for credential",
					"credential_id", cred.ID,
					"provider", string(cred.Type()),
					"error", err)

				// Extract retry time if available and setting it in the error
				if resetTime := ExtractQuotaResetTime(err); !resetTime.IsZero() {
					retryAfter := time.Until(resetTime)
					if retryAfter > 0 {
						// Format as seconds (integer)
						providerErr.RetryAfter = fmt.Sprintf("%.0f", retryAfter.Seconds())
					}
				}

				// Use errorRecorder interface to record the error
				if r.errorRecorder != nil {
					r.errorRecorder.RecordError(cred.ID, req.Model, err)
				}
			}
		}
		// Pass all errors to user (no silent failover retry)
		return nil, err
	}

	// Success: Reset FailureCount to 0 to prevent backoff accumulation across unrelated failures
	if r.errorRecorder != nil {
		r.errorRecorder.RecordSuccess(cred.ID, req.Model)
	}

	return resp, nil
}

func (r *SmartRouterV2) selectCredential(req *Request) (*auth.Credential, error) {
	credInterface, penaltyInfoInterface := r.selector.GetCredentialWithPenaltyInfo(req.Model)

	// Check if all credentials are in penalty
	// Note: penaltyInfoInterface might be a typed nil pointer (*InternalPenaltyInfo(nil)),
	// which is not equal to nil when assigned to interface{}
	if penaltyInfoInterface != nil {
		// Type assert to check if it's actually a nil pointer
		if penaltyInfo, ok := penaltyInfoInterface.(*manager.InternalPenaltyInfo); ok && penaltyInfo != nil {
			// All credentials are in penalty, return 429 error
			message := fmt.Sprintf("All credentials for model '%s' are currently rate limited. Please try again later", req.Model)
			utils.L().Warnf("selectCredential: all credentials in penalty for model %s, returning 429", req.Model)
			return nil, provider.NewProviderErrorWithRetry(
				http.StatusTooManyRequests,
				message,
				"omniproxy",
				nil,
				"",
			)
		}
	}

	// If no credential selected, try fallback
	if credInterface == nil {
		credInterface = r.selector.GetCredential(req.Model)
	}

	if credInterface == nil {
		return nil, fmt.Errorf("no active credentials for model %s", req.Model)
	}

	cred, ok := credInterface.(*auth.Credential)
	if !ok {
		return nil, fmt.Errorf("invalid credential type for model %s", req.Model)
	}

	return cred, nil
}

func (r *SmartRouterV2) executeWithCredential(ctx context.Context, cred *auth.Credential, req *Request) (*Response, error) {
	switch req.Protocol {
	case provider.ProtocolOpenAI:
		return r.executeOpenAI(ctx, cred, req)
	case provider.ProtocolGemini:
		return r.executeGemini(ctx, cred, req)
	default:
		return nil, fmt.Errorf("unsupported protocol: %s", req.Protocol)
	}
}

// executeGemini executes a Gemini-native request using the credential's client
func (r *SmartRouterV2) executeGemini(ctx context.Context, cred *auth.Credential, req *Request) (*Response, error) {
	// Get "Dumb Client" from credential
	clientRaw := cred.GetClient()
	if clientRaw == nil {
		return nil, &provider.ProviderError{
			StatusCode: http.StatusInternalServerError,
			Message:    "Client is nil",
			Provider:   string(cred.Type()),
		}
	}

	client, ok := clientRaw.(*genai.Client)
	if !ok {
		return nil, &provider.ProviderError{
			StatusCode: http.StatusInternalServerError,
			Message:    "Invalid client type",
			Provider:   string(cred.Type()),
		}
	}

	modelName := req.Model
	if req.IsStream {
		return r.executeGeminiStream(ctx, cred, client, modelName, req)
	}
	return r.executeGeminiNonStream(ctx, cred, client, modelName, req)
}

func (r *SmartRouterV2) executeGeminiNonStream(ctx context.Context, cred *auth.Credential, client *genai.Client, modelName string, req *Request) (*Response, error) {
	utils.L().Infow("Starting Gemini non-streaming request (V2)",
		"model", modelName,
		"provider", string(cred.Type()),
		"provider_name", cred.Name())

	payloadMap, ok := req.Payload.(map[string]interface{})
	if !ok {
		return nil, &provider.ProviderError{
			StatusCode: http.StatusBadRequest,
			Message:    "invalid request payload format",
			Provider:   string(cred.Type()),
		}
	}

	// Extract pre-parsed contents and config from the request
	contents, ok := payloadMap["contents"].([]*genai.Content)
	if !ok {
		return nil, &provider.ProviderError{
			StatusCode: http.StatusBadRequest,
			Message:    "contents not found in payload",
			Provider:   string(cred.Type()),
		}
	}

	genConfig, ok := payloadMap["generationConfig"].(*genai.GenerateContentConfig)
	if !ok {
		return nil, &provider.ProviderError{
			StatusCode: http.StatusBadRequest,
			Message:    "generationConfig not found in payload",
			Provider:   string(cred.Type()),
		}
	}

	// Execute the request
	resp, err := client.Models.GenerateContent(ctx, modelName, contents, genConfig)
	if err != nil {
		return nil, r.mapGeminiError(err, cred)
	}

	// Convert response to JSON
	respData, err := json.Marshal(resp)
	if err != nil {
		return nil, &provider.ProviderError{
			StatusCode: http.StatusInternalServerError,
			Message:    fmt.Sprintf("failed to marshal response: %v", err),
			Provider:   string(cred.Type()),
		}
	}

	return &Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(respData)),
		Headers:    map[string]string{"Content-Type": "application/json", "x-goog-api-client": "genai-go"},
	}, nil
}

func (r *SmartRouterV2) executeGeminiStream(ctx context.Context, cred *auth.Credential, client *genai.Client, modelName string, req *Request) (*Response, error) {
	utils.L().Infow("Starting Gemini streaming request (V2)",
		"model", modelName,
		"provider", string(cred.Type()),
		"provider_name", cred.Name())

	payloadMap, ok := req.Payload.(map[string]interface{})
	if !ok {
		return nil, &provider.ProviderError{
			StatusCode: http.StatusBadRequest,
			Message:    "invalid request payload format",
			Provider:   string(cred.Type()),
		}
	}

	// Extract pre-parsed contents and config from the request
	contents, ok := payloadMap["contents"].([]*genai.Content)
	if !ok {
		return nil, &provider.ProviderError{
			StatusCode: http.StatusBadRequest,
			Message:    "contents not found in payload",
			Provider:   string(cred.Type()),
		}
	}

	genConfig, ok := payloadMap["generationConfig"].(*genai.GenerateContentConfig)
	if !ok {
		return nil, &provider.ProviderError{
			StatusCode: http.StatusBadRequest,
			Message:    "generationConfig not found in payload",
			Provider:   string(cred.Type()),
		}
	}

	iter := client.Models.GenerateContentStream(ctx, modelName, contents, genConfig)
	pr, pw := io.Pipe()

	// Use a channel to wait for the first result or error to catch connection issues
	firstResult := make(chan error, 1)

	chunkCount := 0
	firstItemChecked := false

	go func() {
		defer func() {
			utils.L().Infow("Gemini streaming goroutine ending",
				"model", modelName,
				"total_chunks", chunkCount,
				"provider", string(cred.Type()),
				"provider_name", cred.Name())
			pw.Close()
		}()

		for resp, err := range iter {
			if !firstItemChecked {
				firstItemChecked = true
				if err != nil {
					// Check if this is a 429 error and update credential state
					if IsRateLimitError(err) && r.errorRecorder != nil {
						r.errorRecorder.RecordError(cred.ID, req.Model, err)
					}
					firstResult <- err
					return
				}
				firstResult <- nil // Success
			}

			if ctx.Err() != nil {
				utils.L().Infow("Client disconnected during Gemini streaming",
					"model", modelName,
					"chunks_sent", chunkCount,
					"provider", string(cred.Type()),
					"provider_name", cred.Name())
				return
			}

			if err != nil {
				if err.Error() != "EOF" {
					utils.L().Errorw("Gemini stream error mid-stream",
						"model", modelName,
						"error", err,
						"chunks_sent", chunkCount,
						"provider", string(cred.Type()),
						"provider_name", cred.Name())
					// Check if this is a 429 error and update credential state
					if IsRateLimitError(err) && r.errorRecorder != nil {
						r.errorRecorder.RecordError(cred.ID, req.Model, err)
					}
				} else {
					utils.L().Infow("Gemini stream completed normally (EOF)",
						"model", modelName,
						"total_chunks", chunkCount,
						"provider", string(cred.Type()),
						"provider_name", cred.Name())
				}
				return
			}

			if resp == nil {
				utils.L().Debugw("Received nil response in stream, skipping",
					"model", modelName,
					"provider", string(cred.Type()),
					"provider_name", cred.Name())
				continue
			}

			data, err := json.Marshal(resp)
			if err != nil {
				utils.L().Errorw("Failed to marshal Gemini stream response",
					"model", modelName,
					"error", err,
					"provider", string(cred.Type()),
					"provider_name", cred.Name())
				continue
			}

			chunkCount++
			pw.Write([]byte(fmt.Sprintf("data: %s\n\n", string(data))))
		}

		// If the loop finished without any items, signal success (empty stream)
		if !firstItemChecked {
			firstResult <- nil
		}
	}()

	// Wait for the first item or error
	select {
	case err := <-firstResult:
		if err != nil {
			return nil, r.mapGeminiError(err, cred)
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	return &Response{
		StatusCode: http.StatusOK,
		Body:       pr,
		Headers:    map[string]string{"Content-Type": "text/event-stream", "x-goog-api-client": "genai-go"},
	}, nil
}

// executeOpenAI executes an OpenAI-compatible request
func (r *SmartRouterV2) executeOpenAI(ctx context.Context, cred *auth.Credential, req *Request) (*Response, error) {
	// Get provider from credential
	providerInstance := cred.GetProvider()
	if providerInstance == nil {
		return nil, &provider.ProviderError{
			StatusCode: http.StatusInternalServerError,
			Message:    "no provider instance found in credential",
			Provider:   string(cred.Type()),
		}
	}

	// Cast to OpenAICompatibleProvider
	openaiProvider, ok := providerInstance.(provider.OpenAICompatibleProvider)
	if !ok {
		return nil, &provider.ProviderError{
			StatusCode: http.StatusInternalServerError,
			Message:    "provider is not an OpenAICompatibleProvider",
			Provider:   string(cred.Type()),
		}
	}

	// Parse the payload as OpenAI request
	var openAIReq openai.ChatCompletionRequest
	switch p := req.Payload.(type) {
	case *openai.ChatCompletionRequest:
		openAIReq = *p
	case openai.ChatCompletionRequest:
		openAIReq = p
	default:
		// Try to decode from JSON
		data, err := json.Marshal(req.Payload)
		if err != nil {
			return nil, &provider.ProviderError{
				StatusCode: http.StatusBadRequest,
				Message:    fmt.Sprintf("failed to marshal payload: %v", err),
				Provider:   string(cred.Type()),
			}
		}
		if err := json.Unmarshal(data, &openAIReq); err != nil {
			return nil, &provider.ProviderError{
				StatusCode: http.StatusBadRequest,
				Message:    fmt.Sprintf("failed to unmarshal OpenAI request: %v", err),
				Provider:   string(cred.Type()),
			}
		}
	}

	// Override stream setting from request
	openAIReq.Stream = req.IsStream

	// Execute the request based on whether it's streaming
	if openAIReq.Stream {
		return r.executeOpenAIStream(ctx, cred, openaiProvider, openAIReq)
	} else {
		return r.executeOpenAINonStream(ctx, cred, openaiProvider, openAIReq)
	}
}

// executeOpenAINonStream executes a non-streaming OpenAI request
func (r *SmartRouterV2) executeOpenAINonStream(ctx context.Context, cred *auth.Credential, openaiProvider provider.OpenAICompatibleProvider, req openai.ChatCompletionRequest) (*Response, error) {
	utils.L().Infow("Starting OpenAI non-streaming request (V2)",
		"model", req.Model,
		"stream", req.Stream,
		"num_messages", len(req.Messages),
		"provider", string(cred.Type()),
		"provider_name", cred.Name())

	resp, err := openaiProvider.ChatCompletion(ctx, req)
	if err != nil {
		// Return the error as-is - it should already be a ProviderError
		return nil, err
	}

	// Convert response to JSON
	respData, err := json.Marshal(resp)
	if err != nil {
		return nil, &provider.ProviderError{
			StatusCode: http.StatusInternalServerError,
			Message:    fmt.Sprintf("failed to marshal response: %v", err),
			Provider:   "openai",
		}
	}

	return &Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(respData)),
		Headers:    map[string]string{"Content-Type": "application/json"},
	}, nil
}

// executeOpenAIStream executes a streaming OpenAI request
func (r *SmartRouterV2) executeOpenAIStream(ctx context.Context, cred *auth.Credential, openaiProvider provider.OpenAICompatibleProvider, req openai.ChatCompletionRequest) (*Response, error) {
	utils.L().Infow("Starting OpenAI streaming request (V2)",
		"model", req.Model,
		"stream", req.Stream,
		"num_messages", len(req.Messages),
		"provider", string(cred.Type()),
		"provider_name", cred.Name())

	respChan, errChan, err := openaiProvider.StreamChatCompletion(ctx, req)
	if err != nil {
		// Check if this is a 429 error and update credential state
		if IsRateLimitError(err) && r.errorRecorder != nil {
			r.errorRecorder.RecordError(cred.ID, req.Model, err)
		}
		return nil, err
	}

	// Create a pipe to bridge the streaming response
	pr, pw := io.Pipe()

	chunkCount := 0

	// Start a goroutine to read from the stream and write to the pipe
	go func() {
		defer func() {
			utils.L().Infow("Streaming goroutine ending (V2)",
				"model", req.Model,
				"chunks_sent", chunkCount,
				"provider", string(cred.Type()),
				"provider_name", cred.Name())
			pw.Close()
		}()

		for {
			select {
			case <-ctx.Done():
				// Client disconnected, clean up
				utils.L().Infow("Client disconnected during streaming (V2)", "model", req.Model, "provider", string(cred.Type()), "provider_name", cred.Name())
				return
			case err, ok := <-errChan:
				if !ok {
					// errChan closed without error - stream completed successfully
					// Wait for respChan to close to ensure all data is sent
					continue
				}
				if err != nil {
					// Check if this is io.EOF indicating normal stream completion
					if err.Error() == "EOF" {
						// Normal completion - wait for respChan to close
						continue
					}
					// Stream error occurred
					utils.L().Errorw("Stream error (V2)", "model", req.Model, "error", err, "provider", string(cred.Type()), "provider_name", cred.Name())
					// Check if this is a 429 error and update credential state
					if IsRateLimitError(err) && r.errorRecorder != nil {
						r.errorRecorder.RecordError(cred.ID, req.Model, err)
					}
					return
				}
				// Success with nil error - wait for respChan to close
				continue
			case resp, ok := <-respChan:
				if !ok {
					// Stream ended - provider has finished sending all data
					// // Send [DONE] signal to indicate stream termination (standard OpenAI protocol)
					// // Note: This is a legacy feature - some clients may not require this, so it's commented out for now
					// pw.Write([]byte("data: [DONE]\n\n"))
					return
				}

				// Check if response has no content
				if len(resp.Choices) == 0 {
					utils.L().Debugw("Empty response chunk, skipping (V2)", "model", req.Model, "provider", string(cred.Type()), "provider_name", cred.Name())
					continue
				}

				// Marshal the response chunk
				data, err := json.Marshal(resp)
				if err != nil {
					utils.L().Errorw("Failed to marshal response chunk (V2)", "model", req.Model, "error", err, "provider", string(cred.Type()), "provider_name", cred.Name())
					continue // Skip malformed chunks
				}

				// Write the chunk in SSE format
				chunkCount++
				pw.Write([]byte(fmt.Sprintf("data: %s\n\n", string(data))))
			}
		}
	}()

	return &Response{
		StatusCode: http.StatusOK,
		Body:       pr,
		Headers:    map[string]string{"Content-Type": "text/event-stream"},
	}, nil
}

// Helpers

func (r *SmartRouterV2) parseGeminiContents(contentsInterface interface{}) ([]*genai.Content, error) {
	if contentsInterface == nil {
		return nil, fmt.Errorf("contents is required")
	}

	var contents []*genai.Content
	contentsSlice, ok := contentsInterface.([]*genai.Content)
	if ok {
		return contentsSlice, nil
	}

	// Fallback: JSON roundtrip
	contentsBytes, err := json.Marshal(contentsInterface)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal contents: %v", err)
	}
	if err := json.Unmarshal(contentsBytes, &contents); err != nil {
		return nil, fmt.Errorf("failed to unmarshal contents: %v", err)
	}
	return contents, nil
}

func (r *SmartRouterV2) parseGeminiConfig(configInterface interface{}) (*genai.GenerateContentConfig, error) {
	if configInterface == nil {
		return nil, nil
	}

	var genConfig *genai.GenerateContentConfig
	configBytes, err := json.Marshal(configInterface)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal generation config: %v", err)
	}
	if err := json.Unmarshal(configBytes, &genConfig); err != nil {
		return nil, fmt.Errorf("failed to unmarshal generation config: %v", err)
	}
	return genConfig, nil
}

func (r *SmartRouterV2) mapGeminiError(err error, cred *auth.Credential) *provider.ProviderError {
	// Default to 500
	statusCode := http.StatusInternalServerError
	message := err.Error()
	statusStr := ""
	var details interface{} = nil

	// Try to extract status code from genai.APIError
	// Note: err might be genai.APIError (value type) or *genai.APIError (pointer type)
	var apiErr *genai.APIError

	if ptrErr, ok := err.(*genai.APIError); ok {
		apiErr = ptrErr
	} else if valErr, ok := err.(genai.APIError); ok {
		apiErr = &valErr
	}

	if apiErr != nil {
		statusCode = apiErr.Code
		message = apiErr.Message
		statusStr = apiErr.Status
		if len(apiErr.Details) > 0 {
			details = apiErr.Details
		}

		// Extract actual status code from error message if needed
		// genai.APIError.Error() formats as "Error {code}, Message: {message}, Status: {status}, Details: {details}"
		var extractedCode int
		if n, _ := fmt.Sscanf(err.Error(), "Error %d", &extractedCode); n == 1 && extractedCode >= 400 && extractedCode < 600 {
			statusCode = extractedCode
		}
	}

	// Append status string to message for clarity
	if statusStr != "" {
		message = fmt.Sprintf("%s, Status: %s", message, statusStr)
	}

	// Try to extract retry time
	if resetTime := ExtractQuotaResetTime(err); !resetTime.IsZero() {
		// Format as seconds (integer)
		retryAfter := time.Until(resetTime)
		if retryAfter > 0 {
			// Create new error with retry info
			return provider.NewProviderErrorWithRetry(
				statusCode,
				message,
				string(cred.Type()),
				details,
				fmt.Sprintf("%.0f", retryAfter.Seconds()),
			)
		}
	}

	return &provider.ProviderError{
		StatusCode: statusCode,
		Message:    message,
		Details:    details,
		Provider:   string(cred.Type()),
	}
}
