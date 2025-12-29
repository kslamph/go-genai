package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/sunbankio/omniproxy/pkg/utils"
	"google.golang.org/genai"
)

// GeminiRequest represents the incoming Gemini API request structure
type GeminiRequest struct {
	Contents          []*optimizedContent          `json:"contents"`
	Tools             []*genai.Tool                `json:"tools,omitempty"`
	ToolConfig        *genai.ToolConfig            `json:"toolConfig,omitempty"`
	SafetySettings    []*genai.SafetySetting       `json:"safetySettings,omitempty"`
	SystemInstruction *optimizedContent            `json:"systemInstruction,omitempty"`
	GenerationConfig  *genai.GenerateContentConfig `json:"generationConfig,omitempty"`
	CachedContent     string                       `json:"cachedContent,omitempty"`
}

type optimizedContent struct {
	Parts []*optimizedPart `json:"parts,omitempty"`
	Role  string           `json:"role,omitempty"`
}

func (oc *optimizedContent) toGenAI() *genai.Content {
	if oc == nil {
		return nil
	}
	parts := make([]*genai.Part, len(oc.Parts))
	for i, p := range oc.Parts {
		parts[i] = p.toGenAI()
	}
	return &genai.Content{
		Parts: parts,
		Role:  oc.Role,
	}
}

type optimizedPart struct {
	MediaResolution     *genai.PartMediaResolution `json:"mediaResolution,omitempty"`
	CodeExecutionResult *genai.CodeExecutionResult `json:"codeExecutionResult,omitempty"`
	ExecutableCode      *genai.ExecutableCode      `json:"executableCode,omitempty"`
	FileData            *genai.FileData            `json:"fileData,omitempty"`
	FunctionCall        *genai.FunctionCall        `json:"functionCall,omitempty"`
	FunctionResponse    *genai.FunctionResponse    `json:"functionResponse,omitempty"`
	InlineData          *genai.Blob                `json:"inlineData,omitempty"`
	Text                string                     `json:"text,omitempty"`
	Thought             bool                       `json:"thought,omitempty"`
	ThoughtSignature    thoughtSignature           `json:"thoughtSignature,omitempty"`
	VideoMetadata       *genai.VideoMetadata       `json:"videoMetadata,omitempty"`
}

func (op *optimizedPart) toGenAI() *genai.Part {
	if op == nil {
		return nil
	}
	return &genai.Part{
		MediaResolution:     op.MediaResolution,
		CodeExecutionResult: op.CodeExecutionResult,
		ExecutableCode:      op.ExecutableCode,
		FileData:            op.FileData,
		FunctionCall:        op.FunctionCall,
		FunctionResponse:    op.FunctionResponse,
		InlineData:          op.InlineData,
		Text:                op.Text,
		Thought:             op.Thought,
		ThoughtSignature:    []byte(op.ThoughtSignature),
		VideoMetadata:       op.VideoMetadata,
	}
}

type thoughtSignature []byte

func (ts *thoughtSignature) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	// Try base64 decoding
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err == nil {
		*ts = decoded
		return nil
	}
	// If not base64, it's a plain string
	*ts = []byte(s)
	return nil
}

// fixThoughtSignatures is now deprecated and kept for compatibility if needed elsewhere,
// but we should remove its usage from the main handlers.
func fixThoughtSignatures(data any) {
	// ... existing implementation ...
}

// toGenAIConfig converts the request to the SDK's GenerateContentConfig
func (r *GeminiRequest) toGenAIConfig() *genai.GenerateContentConfig {
	config := r.GenerationConfig
	if config == nil {
		config = &genai.GenerateContentConfig{}
	}

	// Map top-level fields to the config if they are present
	if r.SystemInstruction != nil {
		config.SystemInstruction = r.SystemInstruction.toGenAI()
	}
	if len(r.Tools) > 0 {
		config.Tools = r.Tools
		
		// Set toolConfig - use the one from request if present, otherwise add a default
		// This is required by the Gemini API when tools are present
		if r.ToolConfig != nil {
			config.ToolConfig = r.ToolConfig
		} else {
			config.ToolConfig = &genai.ToolConfig{
				FunctionCallingConfig: &genai.FunctionCallingConfig{
					Mode: genai.FunctionCallingConfigModeAuto,
				},
			}
		}
	}
	if len(r.SafetySettings) > 0 {
		config.SafetySettings = r.SafetySettings
	}
	if r.CachedContent != "" {
		config.CachedContent = r.CachedContent
	}

	return config
}

// extractQuotaResetTime extracts the quota reset time from a 429 error message
// Returns the reset time if the error is QUOTA_EXHAUSTED, otherwise returns zero time
func extractQuotaResetTime(err error) time.Time {
	if err == nil {
		return time.Time{}
	}

	errStr := err.Error()
	
	// Check if this is a QUOTA_EXHAUSTED error
	if !strings.Contains(errStr, "QUOTA_EXHAUSTED") {
		return time.Time{}
	}

	// Try to extract the retry delay from the error message
	// The error message format is: "retryDelay:12345.123456789s"
	// We'll parse this to get the reset time
	if strings.Contains(errStr, "retryDelay:") {
		// Find the retryDelay value
		parts := strings.Split(errStr, "retryDelay:")
		if len(parts) > 1 {
			delayStr := strings.TrimSpace(parts[1])
			// Extract the duration (it should end with 's')
			if idx := strings.Index(delayStr, "s"); idx > 0 {
				delayStr = delayStr[:idx]
			}
			// Parse the delay as seconds
			var delaySeconds float64
			if _, err := fmt.Sscanf(delayStr, "%f", &delaySeconds); err == nil {
				resetTime := time.Now().Add(time.Duration(delaySeconds * float64(time.Second)))
				return resetTime
			}
		}
	}

	return time.Time{}
}

func (r *GeminiRequest) toGenAIContents() []*genai.Content {
	contents := make([]*genai.Content, len(r.Contents))
	for i, c := range r.Contents {
		contents[i] = c.toGenAI()
	}
	return contents
}

// Gemini v1beta API Handlers (following https://ai.google.dev/api/all-methods)

// HandleGeminiModels handles GET /{provider}/v1beta/models
func (s *Server) HandleGeminiModels(w http.ResponseWriter, r *http.Request) {
	providerType := chi.URLParam(r, "provider")

	// Only allow gemini and antigravity providers
	if providerType != "gemini" && providerType != "antigravity" {
		utils.L().Warnf("Provider %s not available for Gemini v1beta API", providerType)
		http.Error(w, fmt.Sprintf("provider '%s' is not available for Gemini v1beta API. Available providers: gemini, antigravity", providerType), http.StatusNotFound)
		return
	}

	models, err := s.pm.ListGeminiProviderModels(r.Context(), providerType)
	if err != nil {
		utils.L().Errorf("Failed to list models for provider %s: %v", providerType, err)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// Return models in Gemini API format
	type Model struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName,omitempty"`
		Description string `json:"description,omitempty"`
	}

	type ListModelsResponse struct {
		Models []Model `json:"models"`
	}

	resp := ListModelsResponse{
		Models: make([]Model, 0, len(models)),
	}

	for _, modelName := range models {
		resp.Models = append(resp.Models, Model{
			Name:        "models/" + modelName,
			DisplayName: modelName,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		utils.L().Errorf("Failed to encode Gemini models response: %v", err)
	}
}

// HandleGeminiGenerateContent handles POST /{provider}/v1beta/{model}:generateContent
func (s *Server) HandleGeminiGenerateContent(w http.ResponseWriter, r *http.Request) {
	providerType := chi.URLParam(r, "provider")
	modelName := chi.URLParam(r, "model")

	// Only allow gemini and antigravity providers
	if providerType != "gemini" && providerType != "antigravity" {
		utils.L().Warnf("Provider %s not available for Gemini v1beta API", providerType)
		http.Error(w, fmt.Sprintf("provider '%s' is not available for Gemini v1beta API. Available providers: gemini, antigravity", providerType), http.StatusNotFound)
		return
	}

	// Get Gemini-native provider
	p, err := s.pm.GetGeminiProvider(providerType, modelName)
	if err != nil {
		utils.L().Errorf("Failed to get Gemini provider %s: %v", providerType, err)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// Read body for pre-processing and debug dumping
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		utils.L().Errorf("Failed to read request body: %v", err)
		http.Error(w, "failed to read request body", http.StatusInternalServerError)
		return
	}

	// Parse request body
	var req GeminiRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		s.dumpRequestIfDebug(r, bodyBytes, nil)
		utils.L().Errorf("Failed to decode Gemini request: %v", err)
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Construct final GenerationConfig
	finalConfig := req.toGenAIConfig()

	s.dumpRequestIfDebug(r, bodyBytes, finalConfig)

	// Validate request
	if modelName == "" {
		http.Error(w, "model is required", http.StatusBadRequest)
		return
	}

	if len(req.Contents) == 0 {
		http.Error(w, "contents is required", http.StatusBadRequest)
		return
	}

	utils.L().Debugw("Gemini generate content request", "provider", providerType, "model", modelName,
		"contents_count", len(req.Contents), "tools_count", len(finalConfig.Tools),
		"safety_settings_count", len(finalConfig.SafetySettings), "has_system_instruction", finalConfig.SystemInstruction != nil,
		"has_cached_content", finalConfig.CachedContent != "")

	client := p.GetClient()
	utils.L().Infow("Sending request to provider",
		"provider", p.Name(),
		"provider_type", providerType,
		"model", modelName,
		"stream", false)

	resp, err := client.Models.GenerateContent(r.Context(), modelName, req.toGenAIContents(), finalConfig)
	if err != nil {
		errStr := err.Error()
		// Check if this is a 429 rate limit error
		if strings.Contains(errStr, "429") || strings.Contains(errStr, "RESOURCE_EXHAUSTED") || strings.Contains(errStr, "RATE_LIMIT_EXCEEDED") {
			// Check if this is a QUOTA_EXHAUSTED error (hard quota limit)
			if resetTime := extractQuotaResetTime(err); !resetTime.IsZero() {
				s.pm.RecordQuotaExhausted(p.Name(), modelName, resetTime)
				utils.L().Errorw("Gemini provider failed: 429 quota exhausted - provider excluded until reset",
					"provider", p.Name(),
					"model", modelName,
					"reset_time", resetTime.Format(time.RFC3339),
					"reset_in", time.Until(resetTime).String(),
					"error", err)
			} else {
				// This is a MODEL_CAPACITY_EXHAUSTED error (temporary capacity issue)
				s.pm.RecordRateLimitReject(p.Name(), modelName)
				utils.L().Errorw("Gemini provider failed: 429 rate limit exceeded - recording rejection",
					"provider", p.Name(),
					"model", modelName,
					"error", err)
			}
		} else {
			utils.L().Errorf("Gemini provider %s failed: %v", p.Name(), err)
		}
		s.writeGeminiErrorResponse(w, err)
		return
	}

	utils.L().Debugw("Gemini generate content response", "provider", providerType, "model", modelName,
		"candidates_count", len(resp.Candidates), "has_usage_metadata", resp.UsageMetadata != nil)

	w.Header().Set("x-goog-api-client", "genai-go")
	w.Header().Set("Content-Type", "application/json")
	// Clean the response to remove internal SDK metadata
	cleanResp := cleanGeminiResponse(resp)
	s.dumpResponseIfDebug(r, cleanResp)
	if err := json.NewEncoder(w).Encode(cleanResp); err != nil {
		utils.L().Errorf("Failed to encode Gemini response: %v", err)
	}
}

// HandleGeminiStreamGenerateContent handles POST /{provider}/v1beta/{model}:streamGenerateContent
func (s *Server) HandleGeminiStreamGenerateContent(w http.ResponseWriter, r *http.Request) {
	providerType := chi.URLParam(r, "provider")
	modelName := chi.URLParam(r, "model")

	// Only allow gemini and antigravity providers
	if providerType != "gemini" && providerType != "antigravity" {
		utils.L().Warnf("Provider %s not available for Gemini v1beta API", providerType)
		http.Error(w, fmt.Sprintf("provider '%s' is not available for Gemini v1beta API. Available providers: gemini, antigravity", providerType), http.StatusNotFound)
		return
	}

	// Get Gemini-native provider
	p, err := s.pm.GetGeminiProvider(providerType, modelName)
	if err != nil {
		utils.L().Errorf("Failed to get Gemini provider %s: %v", providerType, err)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// Read body for pre-processing and debug dumping
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		utils.L().Errorf("Failed to read request body: %v", err)
		http.Error(w, "failed to read request body", http.StatusInternalServerError)
		return
	}

	// Parse request body
	var req GeminiRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		s.dumpRequestIfDebug(r, bodyBytes, nil)
		utils.L().Errorf("Failed to decode Gemini request: %v", err)
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Construct final GenerationConfig
	finalConfig := req.toGenAIConfig()

	s.dumpRequestIfDebug(r, bodyBytes, finalConfig)

	// Validate request
	if modelName == "" {
		http.Error(w, "model is required", http.StatusBadRequest)
		return
	}

	if len(req.Contents) == 0 {
		http.Error(w, "contents is required", http.StatusBadRequest)
		return
	}

	client := p.GetClient()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("x-goog-api-client", "genai-go")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	utils.L().Infow("Sending request to provider",
		"provider", p.Name(),
		"provider_type", providerType,
		"model", modelName,
		"stream", true)

	iter := client.Models.GenerateContentStream(r.Context(), modelName, req.toGenAIContents(), finalConfig)

	utils.L().Infow("Starting Gemini stream (SSE)", "provider", providerType, "model", modelName, "contents_count", len(req.Contents))

	chunkCount := 0

	for resp, err := range iter {
		// Handle stream-level error (occurs mid-stream)
		if err != nil {
			errStr := err.Error()
			// Check if this is a 429 rate limit error
			if strings.Contains(errStr, "429") || strings.Contains(errStr, "RESOURCE_EXHAUSTED") || strings.Contains(errStr, "RATE_LIMIT_EXCEEDED") {
				// Check if this is a QUOTA_EXHAUSTED error (hard quota limit)
				if resetTime := extractQuotaResetTime(err); !resetTime.IsZero() {
					s.pm.RecordQuotaExhausted(p.Name(), modelName, resetTime)
					utils.L().Errorw("Gemini stream error: 429 quota exhausted - provider excluded until reset",
						"provider", p.Name(),
						"model", modelName,
						"reset_time", resetTime.Format(time.RFC3339),
						"reset_in", time.Until(resetTime).String(),
						"error", err)
				} else {
					// This is a MODEL_CAPACITY_EXHAUSTED error (temporary capacity issue)
					s.pm.RecordRateLimitReject(p.Name(), modelName)
					utils.L().Errorw("Gemini stream error: 429 rate limit exceeded - recording rejection",
						"provider", p.Name(),
						"model", modelName,
						"error", err)
				}
			} else {
				utils.L().Errorf("Gemini stream error: %v", err)
			}

			// Build structured error response using genai.APIError
			statusCode := http.StatusInternalServerError
			message := err.Error()
			status := ""
			details := []map[string]any{}

			var apiErr genai.APIError
			if errors.As(err, &apiErr) {
				statusCode = apiErr.Code
				message = apiErr.Message
				status = apiErr.Status
				details = apiErr.Details
			}

			errorResp := map[string]interface{}{
				"error": map[string]interface{}{
					"code":    statusCode,
					"message": message,
				},
			}
			if status != "" {
				errorResp["error"].(map[string]interface{})["status"] = status
			}
			if len(details) > 0 {
				errorResp["error"].(map[string]interface{})["details"] = details
			}

			errorObj, _ := json.Marshal(errorResp)
			s.dumpResponseIfDebug(r, map[string]any{"stream_error": err.Error()})
			fmt.Fprintf(w, "data: %s\n\n", string(errorObj))
			flusher.Flush()
			break
		}

		if resp == nil {
			continue
		}
		chunkCount++

		// Clean the response
		cleanResp := cleanGeminiStreamResponse(resp)
		s.dumpResponseIfDebug(r, cleanResp)
		
		data, err := json.Marshal(cleanResp)
		if err != nil {
			utils.L().Errorf("Failed to marshal chunk: %v", err)
			continue
		}

		// Write SSE data line
		if _, err := fmt.Fprintf(w, "data: %s\n\n", string(data)); err != nil {
			utils.L().Errorf("Failed to write chunk: %v", err)
			return
		}
		flusher.Flush()
	}

	utils.L().Infow("Gemini stream completed", "provider", providerType, "model", modelName, "total_chunks", chunkCount)
}

// isNull checks if a raw JSON message represents a null value
// writeGeminiErrorResponse writes an error response for Gemini API
func (s *Server) writeGeminiErrorResponse(w http.ResponseWriter, err error) {
	// Default values
	statusCode := http.StatusInternalServerError
	message := err.Error()
	status := ""
	details := []map[string]any{}

	// Try to extract genai.APIError from the error
	var apiErr genai.APIError
	if errors.As(err, &apiErr) {
		// Use the structured error from genai SDK
		statusCode = apiErr.Code
		message = apiErr.Message
		status = apiErr.Status
		details = apiErr.Details
		utils.L().Warnf("Writing Gemini error response from APIError: status=%d, message=%s, status_string=%s", statusCode, message, status)
	} else {
		// Fallback to using the raw error
		utils.L().Warnf("Writing Gemini error response from raw error: status=%d, message=%s", statusCode, message)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	errorResp := map[string]interface{}{
		"error": map[string]interface{}{
			"code":    statusCode,
			"message": message,
		},
	}

	// Add status field if available
	if status != "" {
		errorResp["error"].(map[string]interface{})["status"] = status
	}

	// Add details field if available
	if len(details) > 0 {
		errorResp["error"].(map[string]interface{})["details"] = details
	}

	if err := json.NewEncoder(w).Encode(errorResp); err != nil {
		utils.L().Errorf("Failed to encode Gemini error response: %v", err)
	}
}

// cleanGeminiResponse removes internal SDK metadata from the response
func cleanGeminiResponse(resp *genai.GenerateContentResponse) map[string]interface{} {
	// Convert to map to remove internal fields
	data, err := json.Marshal(resp)
	if err != nil {
		utils.L().Errorf("Failed to marshal Gemini response for cleaning: %v", err)
		return nil
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		utils.L().Errorf("Failed to unmarshal Gemini response for cleaning: %v", err)
		return nil
	}

	// Remove internal SDK metadata
	delete(result, "sdkHttpResponse")
	delete(result, "sdkOperation")

	return result
}

// cleanGeminiStreamResponse removes internal SDK metadata from streaming response
func cleanGeminiStreamResponse(resp *genai.GenerateContentResponse) map[string]interface{} {
	return cleanGeminiResponse(resp)
}

// indexOf is a helper function to find a substring
func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// HandleGeminiModelsAll handles GET /v1beta/models - lists models from all Gemini providers
func (s *Server) HandleGeminiModelsAll(w http.ResponseWriter, r *http.Request) {
	// Get models from all Gemini providers
	models, err := s.pm.ListGeminiModels(r.Context())
	if err != nil {
		utils.L().Errorf("Failed to list Gemini models: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(models); err != nil {
		utils.L().Errorf("Failed to encode models response: %v", err)
	}
}

// HandleGeminiGenerateContentAll handles POST /v1beta/models/{model}:generateContent
func (s *Server) HandleGeminiGenerateContentAll(w http.ResponseWriter, r *http.Request) {
	modelName := chi.URLParam(r, "model")

	// Read body for pre-processing and debug dumping
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		utils.L().Errorf("Failed to read request body: %v", err)
		http.Error(w, "failed to read request body", http.StatusInternalServerError)
		return
	}

	// Parse request body
	var req GeminiRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		s.dumpRequestIfDebug(r, bodyBytes, nil)
		utils.L().Errorf("Failed to decode Gemini request: %v", err)
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Construct final GenerationConfig
	finalConfig := req.toGenAIConfig()

	s.dumpRequestIfDebug(r, bodyBytes, finalConfig)

	// Validate request
	if modelName == "" {
		http.Error(w, "model is required", http.StatusBadRequest)
		return
	}

	if len(req.Contents) == 0 {
		http.Error(w, "contents is required", http.StatusBadRequest)
		return
	}

	utils.L().Debugw("Gemini generate content request (all providers)", "model", modelName,
		"contents_count", len(req.Contents), "tools_count", len(finalConfig.Tools),
		"safety_settings_count", len(finalConfig.SafetySettings), "has_system_instruction", finalConfig.SystemInstruction != nil,
		"has_cached_content", finalConfig.CachedContent != "")

	// Get any Gemini provider that supports this model
	p, err := s.pm.GetAnyGeminiProviderByModel(modelName)
	if err != nil {
		utils.L().Errorf("Failed to find Gemini provider for model %s: %v", modelName, err)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	client := p.GetClient()
	utils.L().Infow("Sending request to provider",
		"provider", p.Name(),
		"provider_type", "all-gemini",
		"model", modelName,
		"stream", false)

	resp, err := client.Models.GenerateContent(r.Context(), modelName, req.toGenAIContents(), finalConfig)
	if err != nil {
		errStr := err.Error()
		// Check if this is a 429 rate limit error
		if strings.Contains(errStr, "429") || strings.Contains(errStr, "RESOURCE_EXHAUSTED") || strings.Contains(errStr, "RATE_LIMIT_EXCEEDED") {
			// Check if this is a QUOTA_EXHAUSTED error (hard quota limit)
			if resetTime := extractQuotaResetTime(err); !resetTime.IsZero() {
				s.pm.RecordQuotaExhausted(p.Name(), modelName, resetTime)
				utils.L().Errorw("Gemini provider failed: 429 quota exhausted - provider excluded until reset",
					"provider", p.Name(),
					"model", modelName,
					"reset_time", resetTime.Format(time.RFC3339),
					"reset_in", time.Until(resetTime).String(),
					"error", err)
			} else {
				// This is a MODEL_CAPACITY_EXHAUSTED error (temporary capacity issue)
				s.pm.RecordRateLimitReject(p.Name(), modelName)
				utils.L().Errorw("Gemini provider failed: 429 rate limit exceeded - recording rejection",
					"provider", p.Name(),
					"model", modelName,
					"error", err)
			}
		} else {
			utils.L().Errorf("Gemini provider %s failed: %v", p.Name(), err)
		}
		s.writeGeminiErrorResponse(w, err)
		return
	}

	utils.L().Debugw("Gemini generate content response (all providers)", "model", modelName,
		"provider", p.Name(), "candidates_count", len(resp.Candidates), "has_usage_metadata", resp.UsageMetadata != nil)

	w.Header().Set("x-goog-api-client", "genai-go")
	w.Header().Set("Content-Type", "application/json")
	// Clean the response to remove internal SDK metadata
	cleanResp := cleanGeminiResponse(resp)
	s.dumpResponseIfDebug(r, cleanResp)
	if err := json.NewEncoder(w).Encode(cleanResp); err != nil {
		utils.L().Errorf("Failed to encode Gemini response: %v", err)
	}
}

// HandleGeminiStreamGenerateContentAll handles POST /v1beta/models/{model}:streamGenerateContent
func (s *Server) HandleGeminiStreamGenerateContentAll(w http.ResponseWriter, r *http.Request) {
	modelName := chi.URLParam(r, "model")

	// Read body for pre-processing and debug dumping
	utils.L().Debugw("Starting to read request body", "model", modelName)
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		utils.L().Errorf("Failed to read request body: %v", err)
		http.Error(w, "failed to read request body", http.StatusInternalServerError)
		return
	}
	utils.L().Debugw("Finished reading request body", "model", modelName, "body_size", len(bodyBytes))

	// Parse request body
	var req GeminiRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		s.dumpRequestIfDebug(r, bodyBytes, nil)
		utils.L().Errorf("Failed to decode Gemini request: %v", err)
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	utils.L().Debugw("Finished parsing request", "model", modelName, "contents_count", len(req.Contents))

	// Construct final GenerationConfig
	finalConfig := req.toGenAIConfig()

	s.dumpRequestIfDebug(r, bodyBytes, finalConfig)

	// Validate request
	if modelName == "" {
		http.Error(w, "model is required", http.StatusBadRequest)
		return
	}

	if len(req.Contents) == 0 {
		http.Error(w, "contents is required", http.StatusBadRequest)
		return
	}

	// Get any Gemini provider that supports this model
	p, err := s.pm.GetAnyGeminiProviderByModel(modelName)
	if err != nil {
		utils.L().Errorf("Failed to find Gemini provider for model %s: %v", modelName, err)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	utils.L().Debugw("Got provider, about to send request", "model", modelName, "provider", p.Name())

	client := p.GetClient()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("x-goog-api-client", "genai-go")

	flusher, ok := w.(http.Flusher)

	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	utils.L().Infow("Sending request to provider",
		"provider", p.Name(),
		"provider_type", "all-gemini",
		"model", modelName,
		"stream", true)

	iter := client.Models.GenerateContentStream(r.Context(), modelName, req.toGenAIContents(), finalConfig)

	utils.L().Infow("Starting Gemini stream (all providers) (SSE)", "model", modelName, "contents_count", len(req.Contents), "provider", p.Name())

	chunkCount := 0

	for resp, err := range iter {
		if err != nil {
			errStr := err.Error()
			// Check if this is a 429 rate limit error
			if strings.Contains(errStr, "429") || strings.Contains(errStr, "RESOURCE_EXHAUSTED") || strings.Contains(errStr, "RATE_LIMIT_EXCEEDED") {
				// Check if this is a QUOTA_EXHAUSTED error (hard quota limit)
				if resetTime := extractQuotaResetTime(err); !resetTime.IsZero() {
					s.pm.RecordQuotaExhausted(p.Name(), modelName, resetTime)
					utils.L().Errorw("Gemini stream error mid-way: 429 quota exhausted - provider excluded until reset",
						"provider", p.Name(),
						"model", modelName,
						"reset_time", resetTime.Format(time.RFC3339),
						"reset_in", time.Until(resetTime).String(),
						"error", err)
				} else {
					// This is a MODEL_CAPACITY_EXHAUSTED error (temporary capacity issue)
					s.pm.RecordRateLimitReject(p.Name(), modelName)
					utils.L().Errorw("Gemini stream error mid-way: 429 rate limit exceeded - recording rejection",
						"provider", p.Name(),
						"model", modelName,
						"error", err)
				}
			} else {
				utils.L().Errorf("Gemini stream error mid-way: %v", err)
			}

			// Build structured error response using genai.APIError
			statusCode := http.StatusInternalServerError
			message := err.Error()
			status := ""
			details := []map[string]any{}

			var apiErr genai.APIError
			if errors.As(err, &apiErr) {
				statusCode = apiErr.Code
				message = apiErr.Message
				status = apiErr.Status
				details = apiErr.Details
			}

			errorResp := map[string]interface{}{
				"error": map[string]interface{}{
					"code":    statusCode,
					"message": message,
				},
			}
			if status != "" {
				errorResp["error"].(map[string]interface{})["status"] = status
			}
			if len(details) > 0 {
				errorResp["error"].(map[string]interface{})["details"] = details
			}

			errorObj, _ := json.Marshal(errorResp)
			s.dumpResponseIfDebug(r, map[string]any{"stream_error": err.Error()})
			fmt.Fprintf(w, "data: %s\n\n", string(errorObj))
			flusher.Flush()
			break
		}

		if resp == nil {
			continue
		}

		chunkCount++

		// Clean the response to remove internal SDK metadata
		cleanResp := cleanGeminiStreamResponse(resp)
		s.dumpResponseIfDebug(r, cleanResp)
		
		data, err := json.Marshal(cleanResp)
		if err != nil {
			utils.L().Errorf("Failed to marshal chunk: %v", err)
			continue
		}

		if _, err := fmt.Fprintf(w, "data: %s\n\n", string(data)); err != nil {
			utils.L().Errorf("Failed to write chunk: %v", err)
			return
		}
		flusher.Flush()
	}

	utils.L().Infow("Gemini stream completed (all providers)", "model", modelName, "total_chunks", chunkCount, "provider", p.Name())
}

func (s *Server) getDebugDumpPath(r *http.Request) string {
	reqID := middleware.GetReqID(r.Context())
	if reqID == "" {
		reqID = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	safeReqID := strings.ReplaceAll(reqID, "/", "_")
	return fmt.Sprintf("debug_dumps/gemini_req_%s.log", safeReqID)
}

func (s *Server) dumpRequestIfDebug(r *http.Request, reqBody []byte, genConfig any) {
	if !utils.IsDebugMode() {
		return
	}

	_ = os.MkdirAll("debug_dumps", 0755)
	filename := s.getDebugDumpPath(r)
	
	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		utils.L().Errorf("Failed to open debug dump file: %v", err)
		return
	}
	defer f.Close()

	fmt.Fprintf(f, "=== [%s] Raw Request Body ===\n%s\n\n", time.Now().Format(time.RFC3339), string(reqBody))

	if genConfig != nil {
		configBytes, _ := json.MarshalIndent(genConfig, "", "  ")
		fmt.Fprintf(f, "=== Parsed GenerationConfig ===\n%s\n\n", string(configBytes))
	} else {
		fmt.Fprintf(f, "=== Parsed GenerationConfig ===\n(parsing failed or not available)\n\n")
	}
}

func (s *Server) dumpResponseIfDebug(r *http.Request, resp any) {
	if !utils.IsDebugMode() {
		return
	}

	_ = os.MkdirAll("debug_dumps", 0755)
	filename := s.getDebugDumpPath(r)
	
	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		utils.L().Errorf("Failed to open debug dump file for response: %v", err)
		return
	}
	defer f.Close()

	respBytes, _ := json.MarshalIndent(resp, "", "  ")
	fmt.Fprintf(f, "--- [%s] Outgoing Response Chunk ---\n%s\n\n", time.Now().Format(time.RFC3339), string(respBytes))
}

// ============================================================================
// Direct Passthrough Handlers for /genai/v1beta
// These handlers provide minimal processing - only auth, no request/response conversion
// ============================================================================

// HandleGeminiDirectModels handles GET /genai/v1beta/models - direct passthrough
func (s *Server) HandleGeminiDirectModels(w http.ResponseWriter, r *http.Request) {
	// Get any Gemini provider
	p, err := s.pm.GetAnyGeminiProviderByModel("") // Empty model to get any provider
	if err != nil {
		utils.L().Errorf("Failed to get Gemini provider for direct passthrough: %v", err)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// Get models directly from provider
	models, err := p.ListModels(r.Context())
	if err != nil {
		utils.L().Errorf("Failed to list models for direct passthrough: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Return in Gemini API format
	type Model struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName,omitempty"`
		Description string `json:"description,omitempty"`
	}

	type ListModelsResponse struct {
		Models []Model `json:"models"`
	}

	resp := ListModelsResponse{
		Models: make([]Model, 0, len(models)),
	}

	for _, modelName := range models {
		resp.Models = append(resp.Models, Model{
			Name:        "models/" + modelName,
			DisplayName: modelName,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		utils.L().Errorf("Failed to encode direct models response: %v", err)
	}
}

// HandleGeminiDirectGenerateContent handles POST /genai/v1beta/models/{model}:generateContent - direct passthrough
func (s *Server) HandleGeminiDirectGenerateContent(w http.ResponseWriter, r *http.Request) {
	modelName := chi.URLParam(r, "model")

	// Read request body
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		utils.L().Errorf("Failed to read request body: %v", err)
		http.Error(w, "failed to read request body", http.StatusInternalServerError)
		return
	}

	// Get provider that supports this model
	p, err := s.pm.GetAnyGeminiProviderByModel(modelName)
	if err != nil {
		utils.L().Errorf("Failed to get Gemini provider for model %s: %v", modelName, err)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// Log raw request for debugging
	utils.L().Infow("[DIRECT PASSTHROUGH] Sending request",
		"model", modelName,
		"provider", p.Name(),
		"body_size", len(bodyBytes))

	// Parse the request - we use the same GeminiRequest type as existing handlers
	// but don't do any extra processing/conversion beyond what's needed to call the SDK
	var req GeminiRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		utils.L().Errorf("Failed to decode request: %v", err)
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Validate request
	if modelName == "" {
		http.Error(w, "model is required", http.StatusBadRequest)
		return
	}

	if len(req.Contents) == 0 {
		http.Error(w, "contents is required", http.StatusBadRequest)
		return
	}

	// Build config for SDK call
	finalConfig := req.toGenAIConfig()

	// Dump request with config for debugging
	s.dumpRequestIfDebug(r, bodyBytes, finalConfig)

	client := p.GetClient()

	// Log what we're sending to SDK for debugging
	utils.L().Infow("[DIRECT PASSTHROUGH] Calling SDK",
		"model", modelName,
		"provider", p.Name(),
		"contents_count", len(req.Contents),
		"has_system_instruction", finalConfig.SystemInstruction != nil,
		"tools_count", len(finalConfig.Tools),
		"has_tool_config", finalConfig.ToolConfig != nil,
		"safety_settings_count", len(finalConfig.SafetySettings),
		"cached_content", finalConfig.CachedContent != "")

	// Call the SDK directly with minimal processing
	// We only do what's required to call the SDK (convert contents and config)
	resp, err := client.Models.GenerateContent(r.Context(), modelName, req.toGenAIContents(), finalConfig)
	if err != nil {
		errStr := err.Error()
		// Check if this is a 429 rate limit error
		if strings.Contains(errStr, "429") || strings.Contains(errStr, "RESOURCE_EXHAUSTED") || strings.Contains(errStr, "RATE_LIMIT_EXCEEDED") {
			// Check if this is a QUOTA_EXHAUSTED error (hard quota limit)
			if resetTime := extractQuotaResetTime(err); !resetTime.IsZero() {
				s.pm.RecordQuotaExhausted(p.Name(), modelName, resetTime)
				utils.L().Errorw("[DIRECT PASSTHROUGH] Request failed: 429 quota exhausted - provider excluded until reset",
					"provider", p.Name(),
					"model", modelName,
					"reset_time", resetTime.Format(time.RFC3339),
					"reset_in", time.Until(resetTime).String(),
					"error", err)
			} else {
				// This is a MODEL_CAPACITY_EXHAUSTED error (temporary capacity issue)
				s.pm.RecordRateLimitReject(p.Name(), modelName)
				utils.L().Errorw("[DIRECT PASSTHROUGH] Request failed: 429 rate limit exceeded - recording rejection",
					"provider", p.Name(),
					"model", modelName,
					"error", err)
			}
		} else {
			utils.L().Errorf("[DIRECT PASSTHROUGH] Request failed: %v", err)
		}
		s.writeGeminiErrorResponse(w, err)
		return
	}

	// Return response with minimal processing - just remove SDK metadata
	w.Header().Set("x-goog-api-client", "genai-go")
	w.Header().Set("Content-Type", "application/json")
	
	cleanResp := cleanGeminiResponse(resp)
	s.dumpResponseIfDebug(r, cleanResp)
	
	if err := json.NewEncoder(w).Encode(cleanResp); err != nil {
		utils.L().Errorf("Failed to encode response: %v", err)
	}
}

// HandleGeminiDirectStreamGenerateContent handles POST /genai/v1beta/models/{model}:streamGenerateContent - direct passthrough
func (s *Server) HandleGeminiDirectStreamGenerateContent(w http.ResponseWriter, r *http.Request) {
	modelName := chi.URLParam(r, "model")

	// Read request body
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		utils.L().Errorf("Failed to read request body: %v", err)
		http.Error(w, "failed to read request body", http.StatusInternalServerError)
		return
	}

	// Get provider that supports this model
	p, err := s.pm.GetAnyGeminiProviderByModel(modelName)
	if err != nil {
		utils.L().Errorf("Failed to get Gemini provider for model %s: %v", modelName, err)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// Log raw request for debugging
	utils.L().Infow("[DIRECT PASSTHROUGH] Sending stream request",
		"model", modelName,
		"provider", p.Name(),
		"body_size", len(bodyBytes))

	// Parse the request - we use the same GeminiRequest type as existing handlers
	// but don't do any extra processing/conversion beyond what's needed to call the SDK
	var req GeminiRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		utils.L().Errorf("Failed to decode request: %v", err)
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Validate request
	if modelName == "" {
		http.Error(w, "model is required", http.StatusBadRequest)
		return
	}

	if len(req.Contents) == 0 {
		http.Error(w, "contents is required", http.StatusBadRequest)
		return
	}

	// Build config for SDK call
	finalConfig := req.toGenAIConfig()

	// Dump request with config for debugging
	s.dumpRequestIfDebug(r, bodyBytes, finalConfig)

	client := p.GetClient()

	// Log what we're sending to SDK for debugging
	utils.L().Infow("[DIRECT PASSTHROUGH] Calling SDK (stream)",
		"model", modelName,
		"provider", p.Name(),
		"contents_count", len(req.Contents),
		"has_system_instruction", finalConfig.SystemInstruction != nil,
		"tools_count", len(finalConfig.Tools),
		"has_tool_config", finalConfig.ToolConfig != nil,
		"safety_settings_count", len(finalConfig.SafetySettings),
		"cached_content", finalConfig.CachedContent != "")

	// Set streaming headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("x-goog-api-client", "genai-go")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Call the SDK directly with minimal processing
	// We only do what's required to call the SDK (convert contents and config)
	iter := client.Models.GenerateContentStream(r.Context(), modelName, req.toGenAIContents(), finalConfig)

	utils.L().Infow("[DIRECT PASSTHROUGH] Starting stream", "model", modelName, "provider", p.Name())

	chunkCount := 0

	for resp, err := range iter {
		if err != nil {
			errStr := err.Error()
			// Check if this is a 429 rate limit error
			if strings.Contains(errStr, "429") || strings.Contains(errStr, "RESOURCE_EXHAUSTED") || strings.Contains(errStr, "RATE_LIMIT_EXCEEDED") {
				// Check if this is a QUOTA_EXHAUSTED error (hard quota limit)
				if resetTime := extractQuotaResetTime(err); !resetTime.IsZero() {
					s.pm.RecordQuotaExhausted(p.Name(), modelName, resetTime)
					utils.L().Errorw("[DIRECT PASSTHROUGH] Stream error: 429 quota exhausted - provider excluded until reset",
						"provider", p.Name(),
						"model", modelName,
						"reset_time", resetTime.Format(time.RFC3339),
						"reset_in", time.Until(resetTime).String(),
						"error", err)
				} else {
					// This is a MODEL_CAPACITY_EXHAUSTED error (temporary capacity issue)
					s.pm.RecordRateLimitReject(p.Name(), modelName)
					utils.L().Errorw("[DIRECT PASSTHROUGH] Stream error: 429 rate limit exceeded - recording rejection",
						"provider", p.Name(),
						"model", modelName,
						"error", err)
				}
			} else {
				utils.L().Errorf("[DIRECT PASSTHROUGH] Stream error: %v", err)
			}

			// Build structured error response using genai.APIError
			statusCode := http.StatusInternalServerError
			message := err.Error()
			status := ""
			details := []map[string]any{}

			var apiErr genai.APIError
			if errors.As(err, &apiErr) {
				statusCode = apiErr.Code
				message = apiErr.Message
				status = apiErr.Status
				details = apiErr.Details
			}

			errorResp := map[string]interface{}{
				"error": map[string]interface{}{
					"code":    statusCode,
					"message": message,
				},
			}
			if status != "" {
				errorResp["error"].(map[string]interface{})["status"] = status
			}
			if len(details) > 0 {
				errorResp["error"].(map[string]interface{})["details"] = details
			}

			errorObj, _ := json.Marshal(errorResp)
			s.dumpResponseIfDebug(r, map[string]any{"stream_error": err.Error()})
			fmt.Fprintf(w, "data: %s\n\n", string(errorObj))
			flusher.Flush()
			break
		}

		if resp == nil {
			continue
		}

		chunkCount++

		// Return response with minimal processing - just remove SDK metadata
		cleanResp := cleanGeminiStreamResponse(resp)
		s.dumpResponseIfDebug(r, cleanResp)
		
		data, err := json.Marshal(cleanResp)
		if err != nil {
			utils.L().Errorf("Failed to marshal chunk: %v", err)
			continue
		}

		if _, err := fmt.Fprintf(w, "data: %s\n\n", string(data)); err != nil {
			utils.L().Errorf("Failed to write chunk: %v", err)
			return
		}
		flusher.Flush()
	}

	utils.L().Infow("[DIRECT PASSTHROUGH] Stream completed", "model", modelName, "total_chunks", chunkCount)
}