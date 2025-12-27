package api

import (
	"encoding/base64"
	"encoding/json"
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
	Contents          []*genai.Content             `json:"contents"`
	Tools             []*genai.Tool                `json:"tools,omitempty"`
	ToolConfig        *genai.ToolConfig            `json:"toolConfig,omitempty"`
	SafetySettings    []*genai.SafetySetting       `json:"safetySettings,omitempty"`
	SystemInstruction *genai.Content               `json:"systemInstruction,omitempty"`
	GenerationConfig  *genai.GenerateContentConfig `json:"generationConfig,omitempty"`
	CachedContent     string                       `json:"cachedContent,omitempty"`
}

// fixThoughtSignatures recursively finds "thoughtSignature" fields in a map and
// base64 encodes them if they are strings. This is needed because some clients
// send plain strings, but the genai SDK expects []byte (which Go's JSON decoder
// strictly decodes as base64).
func fixThoughtSignatures(data any) {
	switch v := data.(type) {
	case map[string]any:
		for k, val := range v {
			if k == "thoughtSignature" {
				if str, ok := val.(string); ok {
					v[k] = base64.StdEncoding.EncodeToString([]byte(str))
				}
			} else {
				fixThoughtSignatures(val)
			}
		}
	case []any:
		for _, item := range v {
			fixThoughtSignatures(item)
		}
	}
}

// toGenAIConfig converts the request to the SDK's GenerateContentConfig
func (r *GeminiRequest) toGenAIConfig() *genai.GenerateContentConfig {
	config := r.GenerationConfig
	if config == nil {
		config = &genai.GenerateContentConfig{}
	}

	// Map top-level fields to the config if they are present
	if r.SystemInstruction != nil {
		config.SystemInstruction = r.SystemInstruction
	}
	if len(r.Tools) > 0 {
		config.Tools = r.Tools
	}
	if r.ToolConfig != nil {
		config.ToolConfig = r.ToolConfig
	}
	if len(r.SafetySettings) > 0 {
		config.SafetySettings = r.SafetySettings
	}
	if r.CachedContent != "" {
		config.CachedContent = r.CachedContent
	}

	return config
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
	p, err := s.pm.GetGeminiProvider(providerType)
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

	// Pre-process JSON to handle thoughtSignature type mismatch
	var raw map[string]any
	if err := json.Unmarshal(bodyBytes, &raw); err != nil {
		s.dumpRequestIfDebug(r, bodyBytes, nil)
		utils.L().Errorf("Failed to unmarshal raw Gemini request: %v", err)
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	fixThoughtSignatures(raw)
	processedBytes, _ := json.Marshal(raw)

	// Parse processed request body
	var req GeminiRequest
	if err := json.Unmarshal(processedBytes, &req); err != nil {
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
	resp, err := client.Models.GenerateContent(r.Context(), modelName, req.Contents, finalConfig)
	if err != nil {
		utils.L().Errorf("Gemini provider %s failed: %v", p.Name(), err)
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
	p, err := s.pm.GetGeminiProvider(providerType)
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

	// Pre-process JSON to handle thoughtSignature type mismatch
	var raw map[string]any
	if err := json.Unmarshal(bodyBytes, &raw); err != nil {
		s.dumpRequestIfDebug(r, bodyBytes, nil)
		utils.L().Errorf("Failed to unmarshal raw Gemini request: %v", err)
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	fixThoughtSignatures(raw)
	processedBytes, _ := json.Marshal(raw)

	// Parse processed request body
	var req GeminiRequest
	if err := json.Unmarshal(processedBytes, &req); err != nil {
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

	iter := client.Models.GenerateContentStream(r.Context(), modelName, req.Contents, finalConfig)

	utils.L().Infow("Starting Gemini stream (SSE)", "provider", providerType, "model", modelName, "contents_count", len(req.Contents))

	chunkCount := 0

	for resp, err := range iter {
		// Handle stream-level error (occurs mid-stream)
		if err != nil {
			utils.L().Errorf("Gemini stream error: %v", err)
			// Try to send error event
			errorObj, _ := json.Marshal(map[string]any{
				"error": map[string]any{"message": err.Error(), "code": 500},
			})
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
	statusCode := http.StatusInternalServerError
	message := err.Error()

	// Try to extract status code from error string if it's in genai error format
	errStr := err.Error()
	if idx := indexOf(errStr, "Error "); idx >= 0 {
		var code int
		if _, scanErr := fmt.Sscanf(errStr[idx:], "Error %d", &code); scanErr == nil {
			statusCode = code
		}
	}

	utils.L().Warnf("Writing Gemini error response: status=%d, message=%s", statusCode, message)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	errorResp := map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
			"code":    statusCode,
		},
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

	// Pre-process JSON to handle thoughtSignature type mismatch
	var raw map[string]any
	if err := json.Unmarshal(bodyBytes, &raw); err != nil {
		s.dumpRequestIfDebug(r, bodyBytes, nil)
		utils.L().Errorf("Failed to unmarshal raw Gemini request: %v", err)
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	fixThoughtSignatures(raw)
	processedBytes, _ := json.Marshal(raw)

	// Parse processed request body
	var req GeminiRequest
	if err := json.Unmarshal(processedBytes, &req); err != nil {
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
	resp, err := client.Models.GenerateContent(r.Context(), modelName, req.Contents, finalConfig)
	if err != nil {
		utils.L().Errorf("Gemini provider %s failed: %v", p.Name(), err)
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
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		utils.L().Errorf("Failed to read request body: %v", err)
		http.Error(w, "failed to read request body", http.StatusInternalServerError)
		return
	}

	// Pre-process JSON to handle thoughtSignature type mismatch
	var raw map[string]any
	if err := json.Unmarshal(bodyBytes, &raw); err != nil {
		s.dumpRequestIfDebug(r, bodyBytes, nil)
		utils.L().Errorf("Failed to unmarshal raw Gemini request: %v", err)
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	fixThoughtSignatures(raw)
	processedBytes, _ := json.Marshal(raw)

	// Parse processed request body
	var req GeminiRequest
	if err := json.Unmarshal(processedBytes, &req); err != nil {
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

	// Get any Gemini provider that supports this model
	p, err := s.pm.GetAnyGeminiProviderByModel(modelName)
	if err != nil {
		utils.L().Errorf("Failed to find Gemini provider for model %s: %v", modelName, err)
		http.Error(w, err.Error(), http.StatusNotFound)
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

	iter := client.Models.GenerateContentStream(r.Context(), modelName, req.Contents, finalConfig)

	utils.L().Infow("Starting Gemini stream (all providers) (SSE)", "model", modelName, "contents_count", len(req.Contents), "provider", p.Name())

	chunkCount := 0

	for resp, err := range iter {
		if err != nil {
			utils.L().Errorf("Gemini stream error mid-way: %v", err)
			// Mid-stream error handling
			errorObj, _ := json.Marshal(map[string]any{
				"error": map[string]any{"message": err.Error(), "code": 500},
			})
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