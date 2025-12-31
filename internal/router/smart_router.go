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

// PenaltyBox tracks failed credentials and implements backoff logic
type PenaltyBox struct {
	// entries maps credential ID to penalty information
	entries map[string]*PenaltyEntry
}

// PenaltyEntry contains penalty information for a credential
type PenaltyEntry struct {
	Until        time.Time
	FailureCount int
	LastFailure  time.Time
}

// NewPenaltyBox creates a new penalty box instance
func NewPenaltyBox() *PenaltyBox {
	return &PenaltyBox{
		entries: make(map[string]*PenaltyEntry),
	}
}

// IsInPenaltyBox checks if a credential is currently in the penalty box
func (pb *PenaltyBox) IsInPenaltyBox(credID string) bool {
	entry, exists := pb.entries[credID]
	if !exists {
		return false
	}

	// Clean up expired entries
	if time.Now().After(entry.Until) {
		delete(pb.entries, credID)
		return false
	}

	return true
}

// AddToPenaltyBox adds a credential to the penalty box with exponential backoff
func (pb *PenaltyBox) AddToPenaltyBox(credID string, failureCount int) {
	// Exponential backoff: 1min, 2min, 4min, 8min, max 30min
	backoffDuration := time.Duration(1<<min(failureCount, 4)) * time.Minute
	if backoffDuration > 30*time.Minute {
		backoffDuration = 30 * time.Minute
	}

	pb.entries[credID] = &PenaltyEntry{
		Until:        time.Now().Add(backoffDuration),
		FailureCount: failureCount,
		LastFailure:  time.Now(),
	}
}

// RemoveFromPenaltyBox removes a credential from the penalty box
func (pb *PenaltyBox) RemoveFromPenaltyBox(credID string) {
	delete(pb.entries, credID)
}

// GetFailureCount returns the failure count for a credential
func (pb *PenaltyBox) GetFailureCount(credID string) int {
	entry, exists := pb.entries[credID]
	if !exists {
		return 0
	}
	return entry.FailureCount
}

// SmartRouter handles intelligent routing of requests to providers with retry logic
type SmartRouter struct {
	authManager  auth.AuthManager
	registry     *manager.ProviderRegistry
	loadBalancer *manager.LoadBalancer
	penaltyBox   *PenaltyBox
}

// NewSmartRouter creates a new SmartRouter instance
func NewSmartRouter(authManager auth.AuthManager, registry *manager.ProviderRegistry, loadBalancer *manager.LoadBalancer) *SmartRouter {
	return &SmartRouter{
		authManager:  authManager,
		registry:     registry,
		loadBalancer: loadBalancer,
		penaltyBox:   NewPenaltyBox(),
	}
}

// const (
// 	MaxRetries = 3
// )

// Execute executes a request without retries - clients handle error retry logic
func (r *SmartRouter) Execute(ctx context.Context, req *Request) (*Response, error) {
	// Get candidate credential based on protocol and model
	cred, err := r.selectCredential(req)
	if err != nil {
		return nil, err
	}

	// Ensure we have a valid token (lazy check)
	// Skip this check if the credential has a provider instance, as the provider handles its own authentication
	if cred.GetProvider() == nil {
		if err := r.authManager.EnsureValidToken(ctx, cred); err != nil {
			// Mark credential as having issues and return error
			r.handleCredentialError(cred, err)
			return nil, err
		}
	}

	// Execute the request with the selected credential
	resp, err := r.executeWithCredential(ctx, cred, req)
	if err == nil {
		// Success - record it and return
		r.loadBalancer.RecordSuccess(req.Model, cred)
		r.penaltyBox.RemoveFromPenaltyBox(cred.ID)
		return resp, nil
	}

	// Handle error cases - record and return without retrying
	if providerErr, ok := err.(*provider.ProviderError); ok {
		switch providerErr.StatusCode {
		case http.StatusUnauthorized: // 401
			// Force refresh and return error - client will retry
			if refreshErr := r.authManager.ForceRefresh(ctx, cred); refreshErr != nil {
				r.handleCredentialError(cred, refreshErr)
			}

			// For genai-based providers, refresh the client to pick up new token
			if cred.ProviderType == auth.ProviderTypeGemini || cred.ProviderType == auth.ProviderTypeAntigravity {
				r.authManager.RefreshClient(ctx, cred)
			}

		case http.StatusTooManyRequests: // 429
			// Add to penalty box and return error - client will retry
			failureCount := r.penaltyBox.GetFailureCount(cred.ID) + 1
			r.penaltyBox.AddToPenaltyBox(cred.ID, failureCount)
			r.loadBalancer.RecordFailure(cred)

		default:
			// For 5xx and other errors, record failure and return error - client will retry
			if providerErr.StatusCode >= 500 {
				r.loadBalancer.RecordFailure(cred)
			}
		}
	} else {
		// Non-provider errors, record failure and return error
		r.loadBalancer.RecordFailure(cred)
	}

	return nil, err
}

// selectCredential selects the best credential for the request
func (r *SmartRouter) selectCredential(req *Request) (*auth.Credential, error) {
	// Determine which provider types support the requested protocol
	var providerTypes []provider.ProviderType

	// Check if there's a preferred provider in headers
	preferredProvider := ""
	if pref, exists := req.Headers["X-Preferred-Provider"]; exists {
		preferredProvider = pref
	}

	switch req.Protocol {
	case provider.ProtocolOpenAI:
		if preferredProvider != "" {
			// Use only the preferred provider type
			switch preferredProvider {
			case "qwen":
				providerTypes = []provider.ProviderType{provider.ProviderQwen}
			case "iflow":
				providerTypes = []provider.ProviderType{provider.ProviderIFlow}
			case "openai":
				providerTypes = []provider.ProviderType{provider.ProviderOpenAI}
			default:
				// Fallback to all if unknown
				providerTypes = []provider.ProviderType{
					provider.ProviderQwen,
					provider.ProviderIFlow,
					provider.ProviderOpenAI,
				}
			}
		} else {
			providerTypes = []provider.ProviderType{
				provider.ProviderQwen,
				provider.ProviderIFlow,
				provider.ProviderOpenAI,
			}
		}
	case provider.ProtocolGemini:
		if preferredProvider != "" {
			// Use only the preferred provider type
			switch preferredProvider {
			case "gemini":
				providerTypes = []provider.ProviderType{provider.ProviderGemini}
			case "antigravity":
				providerTypes = []provider.ProviderType{provider.ProviderAntigravity}
			default:
				// Fallback to all if unknown
				providerTypes = []provider.ProviderType{
					provider.ProviderGemini,
					provider.ProviderAntigravity,
				}
			}
		} else {
			providerTypes = []provider.ProviderType{
				provider.ProviderGemini,
				provider.ProviderAntigravity,
			}
		}
	default:
		return nil, fmt.Errorf("unsupported protocol: %s", req.Protocol)
	}

	// Collect all available credentials that support the protocol and aren't in penalty box
	var candidates []*auth.Credential
	for _, pType := range providerTypes {
		credentials := r.registry.GetCredentials(pType)
		for _, cred := range credentials {
			// Skip credentials in penalty box
			if r.penaltyBox.IsInPenaltyBox(cred.ID) {
				continue
			}

			// Check if credential supports the requested protocol
			supported := false
			for _, protocol := range cred.SupportedProtocols() {
				if protocol == req.Protocol {
					supported = true
					break
				}
			}

			if supported && cred.SupportsModel(req.Model) {
				candidates = append(candidates, cred)
			}
		}
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no available credentials for protocol %s and model %s", req.Protocol, req.Model)
	}

	// Convert credentials to BaseProvider interface for load balancer
	providers := make([]provider.BaseProvider, len(candidates))
	for i, cred := range candidates {
		providers[i] = cred
	}

	// Use load balancer to select the best provider
	// For simplicity, we'll use round-robin selection across all provider types
	selectedProvider := r.loadBalancer.SelectRoundRobin(providers, "")
	if selectedProvider == nil {
		return nil, fmt.Errorf("load balancer returned nil provider")
	}

	// Find the corresponding credential
	for _, cred := range candidates {
		if cred.Name() == selectedProvider.Name() {
			return cred, nil
		}
	}

	return nil, fmt.Errorf("selected provider not found in candidates")
}

// executeWithCredential executes the request using a specific credential
func (r *SmartRouter) executeWithCredential(ctx context.Context, cred *auth.Credential, req *Request) (*Response, error) {
	switch req.Protocol {
	case provider.ProtocolOpenAI:
		return r.executeOpenAI(ctx, cred, req)
	case provider.ProtocolGemini:
		return r.executeGemini(ctx, cred, req)
	default:
		return nil, fmt.Errorf("unsupported protocol: %s", req.Protocol)
	}
}

// executeOpenAI executes an OpenAI-compatible request
func (r *SmartRouter) executeOpenAI(ctx context.Context, cred *auth.Credential, req *Request) (*Response, error) {
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
func (r *SmartRouter) executeOpenAINonStream(ctx context.Context, cred *auth.Credential, openaiProvider provider.OpenAICompatibleProvider, req openai.ChatCompletionRequest) (*Response, error) {
	utils.L().Infow("Starting OpenAI non-streaming request",
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
func (r *SmartRouter) executeOpenAIStream(ctx context.Context, cred *auth.Credential, openaiProvider provider.OpenAICompatibleProvider, req openai.ChatCompletionRequest) (*Response, error) {
	utils.L().Infow("Starting OpenAI streaming request",
		"model", req.Model,
		"stream", req.Stream,
		"num_messages", len(req.Messages),
		"provider", string(cred.Type()),
		"provider_name", cred.Name())

	respChan, errChan := openaiProvider.StreamChatCompletion(ctx, req)

	// Create a pipe to bridge the streaming response
	pr, pw := io.Pipe()

	chunkCount := 0

	// Start a goroutine to read from the stream and write to the pipe
	go func() {
		defer func() {
			utils.L().Infow("Streaming goroutine ending",
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
				utils.L().Infow("Client disconnected during streaming", "model", req.Model, "provider", string(cred.Type()), "provider_name", cred.Name())
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
					utils.L().Errorw("Stream error", "model", req.Model, "error", err, "provider", string(cred.Type()), "provider_name", cred.Name())
					return
				}
				// Success with nil error - wait for respChan to close
				continue
			case resp, ok := <-respChan:
				if !ok {
					// Stream ended - provider has finished sending all data
					utils.L().Infow("respChan closed, stream complete", "model", req.Model, "total_chunks", chunkCount, "provider", string(cred.Type()), "provider_name", cred.Name())
					return
				}

				// Check if response has no content
				if len(resp.Choices) == 0 {
					utils.L().Debugw("Empty response chunk, skipping", "model", req.Model, "provider", string(cred.Type()), "provider_name", cred.Name())
					continue
				}

				// Marshal the response chunk
				data, err := json.Marshal(resp)
				if err != nil {
					utils.L().Errorw("Failed to marshal response chunk", "model", req.Model, "error", err, "provider", string(cred.Type()), "provider_name", cred.Name())
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

// executeGemini executes a Gemini-native request
func (r *SmartRouter) executeGemini(ctx context.Context, cred *auth.Credential, req *Request) (*Response, error) {
	// Get provider from credential
	providerInstance := cred.GetProvider()
	if providerInstance == nil {
		return nil, &provider.ProviderError{
			StatusCode: http.StatusInternalServerError,
			Message:    "no provider instance found in credential",
			Provider:   string(cred.Type()),
		}
	}

	// Cast to GeminiNativeProvider
	geminiProvider, ok := providerInstance.(provider.GeminiNativeProvider)
	if !ok {
		return nil, &provider.ProviderError{
			StatusCode: http.StatusInternalServerError,
			Message:    "provider is not a GeminiNativeProvider",
			Provider:   string(cred.Type()),
		}
	}

	// Get genai client from provider
	client := geminiProvider.GetClient()
	if client == nil {
		return nil, &provider.ProviderError{
			StatusCode: http.StatusInternalServerError,
			Message:    "failed to get Gemini client from provider",
			Provider:   string(cred.Type()),
		}
	}

	// Extract model name from headers or payload
	modelName := req.Model
	if modelName == "" {
		// Try to extract from payload if it's a map
		if payloadMap, ok := req.Payload.(map[string]interface{}); ok {
			if model, exists := payloadMap["model"]; exists {
				if modelStr, ok := model.(string); ok {
					modelName = modelStr
				}
			}
		}
	}

	if modelName == "" {
		return nil, &provider.ProviderError{
			StatusCode: http.StatusBadRequest,
			Message:    "model is required for Gemini requests",
			Provider:   string(cred.Type()),
		}
	}

	// For now, we'll implement a basic version that handles the request
	// In a full implementation, we would need to parse the Gemini request format
	// and convert it to genai SDK calls

	// Check if this is a streaming request
	isStream := req.IsStream

	// For demonstration, we'll return a simple response
	// In a real implementation, you would:
	// 1. Parse the Gemini request from req.Payload
	// 2. Convert it to genai SDK format
	// 3. Call client.Models.GenerateContent or client.Models.GenerateContentStream
	// 4. Convert the response back to Gemini API format

	if isStream {
		return r.executeGeminiStream(ctx, cred, client, modelName, req)
	} else {
		return r.executeGeminiNonStream(ctx, cred, client, modelName, req)
	}
}

// executeGeminiNonStream executes a non-streaming Gemini request
func (r *SmartRouter) executeGeminiNonStream(ctx context.Context, cred *auth.Credential, client interface{}, modelName string, req *Request) (*Response, error) {
	utils.L().Infow("Starting Gemini non-streaming request",
		"model", modelName,
		"provider", string(cred.Type()),
		"provider_name", cred.Name())

	genaiClient, ok := client.(*genai.Client)
	if !ok {
		return nil, &provider.ProviderError{
			StatusCode: http.StatusInternalServerError,
			Message:    "invalid client type for Gemini provider",
			Provider:   "gemini",
		}
	}

	// Extract request data from payload
	payloadMap, ok := req.Payload.(map[string]interface{})
	if !ok {
		return nil, &provider.ProviderError{
			StatusCode: http.StatusBadRequest,
			Message:    "invalid request payload format",
			Provider:   "gemini",
		}
	}

	// Extract contents
	contentsInterface, ok := payloadMap["contents"]
	if !ok {
		return nil, &provider.ProviderError{
			StatusCode: http.StatusBadRequest,
			Message:    "contents is required",
			Provider:   "gemini",
		}
	}

	// Convert to genai.Content slice
	var contents []*genai.Content
	contentsSlice, ok := contentsInterface.([]*genai.Content)
	if ok {
		contents = contentsSlice
	} else {
		// Try to convert from JSON
		contentsBytes, err := json.Marshal(contentsInterface)
		if err != nil {
			return nil, &provider.ProviderError{
				StatusCode: http.StatusBadRequest,
				Message:    fmt.Sprintf("failed to marshal contents: %v", err),
				Provider:   "gemini",
			}
		}
		if err := json.Unmarshal(contentsBytes, &contents); err != nil {
			return nil, &provider.ProviderError{
				StatusCode: http.StatusBadRequest,
				Message:    fmt.Sprintf("failed to unmarshal contents: %v", err),
				Provider:   "gemini",
			}
		}
	}

	// Extract generation config
	var genConfig *genai.GenerateContentConfig
	if configInterface, ok := payloadMap["generationConfig"]; ok {
		configBytes, err := json.Marshal(configInterface)
		if err != nil {
			return nil, &provider.ProviderError{
				StatusCode: http.StatusBadRequest,
				Message:    fmt.Sprintf("failed to marshal generation config: %v", err),
				Provider:   "gemini",
			}
		}
		if err := json.Unmarshal(configBytes, &genConfig); err != nil {
			return nil, &provider.ProviderError{
				StatusCode: http.StatusBadRequest,
				Message:    fmt.Sprintf("failed to unmarshal generation config: %v", err),
				Provider:   "gemini",
			}
		}
	}

	// Execute the request
	resp, err := genaiClient.Models.GenerateContent(ctx, modelName, contents, genConfig)
	if err != nil {
		// Try to extract status code from error
		statusCode := http.StatusInternalServerError
		if apiErr, ok := err.(*genai.APIError); ok {
			statusCode = apiErr.Code
		}

		return nil, &provider.ProviderError{
			StatusCode: statusCode,
			Message:    err.Error(),
			Provider:   "gemini",
		}
	}

	// Convert response to JSON
	respData, err := json.Marshal(resp)
	if err != nil {
		return nil, &provider.ProviderError{
			StatusCode: http.StatusInternalServerError,
			Message:    fmt.Sprintf("failed to marshal response: %v", err),
			Provider:   "gemini",
		}
	}

	return &Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(respData)),
		Headers:    map[string]string{"Content-Type": "application/json", "x-goog-api-client": "genai-go"},
	}, nil
}

// executeGeminiStream executes a streaming Gemini request
func (r *SmartRouter) executeGeminiStream(ctx context.Context, cred *auth.Credential, client interface{}, modelName string, req *Request) (*Response, error) {
	utils.L().Infow("Starting Gemini streaming request",
		"model", modelName,
		"provider", string(cred.Type()),
		"provider_name", cred.Name())

	genaiClient, ok := client.(*genai.Client)
	if !ok {
		return nil, &provider.ProviderError{
			StatusCode: http.StatusInternalServerError,
			Message:    "invalid client type for Gemini provider",
			Provider:   "gemini",
		}
	}

	// Extract request data from payload
	payloadMap, ok := req.Payload.(map[string]interface{})
	if !ok {
		return nil, &provider.ProviderError{
			StatusCode: http.StatusBadRequest,
			Message:    "invalid request payload format",
			Provider:   "gemini",
		}
	}

	// Extract contents
	contentsInterface, ok := payloadMap["contents"]
	if !ok {
		return nil, &provider.ProviderError{
			StatusCode: http.StatusBadRequest,
			Message:    "contents is required",
			Provider:   "gemini",
		}
	}

	// Convert to genai.Content slice
	var contents []*genai.Content
	contentsSlice, ok := contentsInterface.([]*genai.Content)
	if ok {
		contents = contentsSlice
	} else {
		// Try to convert from JSON
		contentsBytes, err := json.Marshal(contentsInterface)
		if err != nil {
			return nil, &provider.ProviderError{
				StatusCode: http.StatusBadRequest,
				Message:    fmt.Sprintf("failed to marshal contents: %v", err),
				Provider:   "gemini",
			}
		}
		if err := json.Unmarshal(contentsBytes, &contents); err != nil {
			return nil, &provider.ProviderError{
				StatusCode: http.StatusBadRequest,
				Message:    fmt.Sprintf("failed to unmarshal contents: %v", err),
				Provider:   "gemini",
			}
		}
	}

	// Extract generation config
	var genConfig *genai.GenerateContentConfig
	if configInterface, ok := payloadMap["generationConfig"]; ok {
		configBytes, err := json.Marshal(configInterface)
		if err != nil {
			return nil, &provider.ProviderError{
				StatusCode: http.StatusBadRequest,
				Message:    fmt.Sprintf("failed to marshal generation config: %v", err),
				Provider:   "gemini",
			}
		}
		if err := json.Unmarshal(configBytes, &genConfig); err != nil {
			return nil, &provider.ProviderError{
				StatusCode: http.StatusBadRequest,
				Message:    fmt.Sprintf("failed to unmarshal generation config: %v", err),
				Provider:   "gemini",
			}
		}
	}

	// Create streaming iterator
	iter := genaiClient.Models.GenerateContentStream(ctx, modelName, contents, genConfig)

	// Create a pipe to bridge the streaming response
	pr, pw := io.Pipe()

	// Start a goroutine to read from the stream and write to the pipe
	go func() {
		defer func() {
			utils.L().Infow("Gemini streaming goroutine ending",
				"model", modelName,
				"provider", string(cred.Type()),
				"provider_name", cred.Name())
			pw.Close()
		}()

		chunkCount := 0
		firstItemChecked := false

		for resp, err := range iter {
			select {
			case <-ctx.Done():
				// Client disconnected, clean up
				utils.L().Infow("Client disconnected during Gemini streaming", "model", modelName, "provider", string(cred.Type()), "provider_name", cred.Name())
				return
			default:
			}

			if !firstItemChecked {
				firstItemChecked = true
				if err != nil {
					// First item is an error - send error response and close
					utils.L().Errorw("Gemini stream error on first item", "model", modelName, "error", err, "provider", string(cred.Type()), "provider_name", cred.Name())

					// Parse error to determine status code
					statusCode := http.StatusInternalServerError
					errMsg := err.Error()

					if strings.Contains(errMsg, "429") || strings.Contains(errMsg, "RESOURCE_EXHAUSTED") || strings.Contains(errMsg, "RATE_LIMIT_EXCEEDED") {
						statusCode = http.StatusTooManyRequests
					} else if strings.Contains(errMsg, "401") || strings.Contains(errMsg, "UNAUTHENTICATED") {
						statusCode = http.StatusUnauthorized
					} else if strings.Contains(errMsg, "403") || strings.Contains(errMsg, "PERMISSION_DENIED") {
						statusCode = http.StatusForbidden
					}

					// Create JSON error response
					errorResp := map[string]interface{}{
						"error": map[string]interface{}{
							"code":    statusCode,
							"message": errMsg,
							"status":  http.StatusText(statusCode),
						},
					}
					errorBody, _ := json.Marshal(errorResp)

					// Write error as a single SSE event
					pw.Write([]byte(fmt.Sprintf("data: %s\n\n", string(errorBody))))
					return
				}
			}

			if err != nil {
				// Stream ended or error occurred (after first item)
				if err.Error() != "EOF" {
					// This is an actual error mid-stream
					utils.L().Errorw("Gemini stream error mid-stream", "model", modelName, "error", err, "provider", string(cred.Type()), "provider_name", cred.Name())
					return
				}
				// Stream completed normally
				utils.L().Infow("Gemini respChan closed, stream complete", "model", modelName, "total_chunks", chunkCount, "provider", string(cred.Type()), "provider_name", cred.Name())
				return
			}

			if resp == nil {
				continue
			}

			// Marshal the response chunk
			data, err := json.Marshal(resp)
			if err != nil {
				utils.L().Errorw("Failed to marshal Gemini response chunk", "model", modelName, "error", err, "provider", string(cred.Type()), "provider_name", cred.Name())
				continue // Skip malformed chunks
			}

			// Write the chunk in SSE format
			chunkCount++
			pw.Write([]byte(fmt.Sprintf("data: %s\n\n", string(data))))
		}
	}()

	return &Response{
		StatusCode: http.StatusOK,
		Body:       pr,
		Headers:    map[string]string{"Content-Type": "text/event-stream", "x-goog-api-client": "genai-go"},
	}, nil
}

// handleCredentialError handles errors related to credentials
func (r *SmartRouter) handleCredentialError(cred *auth.Credential, err error) {
	// Record failure
	r.loadBalancer.RecordFailure(cred)

	// Add to penalty box if it's a rate limit or server error
	if providerErr, ok := err.(*provider.ProviderError); ok {
		if providerErr.StatusCode == http.StatusTooManyRequests ||
			providerErr.StatusCode >= 500 {
			failureCount := r.penaltyBox.GetFailureCount(cred.ID) + 1
			r.penaltyBox.AddToPenaltyBox(cred.ID, failureCount)
		}
	}
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
