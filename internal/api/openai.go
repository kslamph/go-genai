package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sashabaranov/go-openai"
	"github.com/sunbankio/omniproxy/internal/provider"
	"github.com/sunbankio/omniproxy/internal/router"
	"github.com/sunbankio/omniproxy/pkg/utils"
)

func (s *Server) HandleProviderListModels(w http.ResponseWriter, r *http.Request) {
	providerType := chi.URLParam(r, "provider")

	// Only allow qwen and iflow providers for OpenAI-compatible API
	pType := provider.ProviderType(providerType)
	if pType != provider.ProviderQwen && pType != provider.ProviderIFlow {
		utils.L().Warnf("Provider %s not available for OpenAI-compatible API", providerType)
		http.Error(w, fmt.Sprintf("provider '%s' is not available for OpenAI-compatible API. Available providers: qwen, iflow", providerType), http.StatusNotFound)
		return
	}

	models, err := s.ps.ListOpenAIProviderModels(r.Context(), pType)
	if err != nil {
		utils.L().Errorf("Failed to list models for provider %s: %v", providerType, err)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	s.writeModelsResponse(w, models)
}

func (s *Server) writeModelsResponse(w http.ResponseWriter, modelNames []string) {
	type modelResponse struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}

	type listResponse struct {
		Object string          `json:"object"`
		Data   []modelResponse `json:"data"`
	}

	resp := listResponse{
		Object: "list",
		Data:   make([]modelResponse, 0, len(modelNames)),
	}

	for _, name := range modelNames {
		resp.Data = append(resp.Data, modelResponse{
			ID:      name,
			Object:  "model",
			Created: time.Now().Unix(),
			OwnedBy: "omniproxy",
		})
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		utils.L().Errorf("Failed to encode models response: %v", err)
	}
}

func (s *Server) HandleChat(w http.ResponseWriter, r *http.Request) {
	// Read the request body
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	// Restore the body for decoding
	r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	var req openai.ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.L().Errorf("Failed to decode request: %v", err)
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Log the decoded request (debug only)
	utils.L().Debugw("Decoded request", "num_messages", len(req.Messages), "model", req.Model)
	for i, msg := range req.Messages {
		// Extract text from either Content (string) or MultiContent (array)
		contentText := msg.Content
		if contentText == "" && len(msg.MultiContent) > 0 {
			// Concatenate all text parts from MultiContent
			for _, part := range msg.MultiContent {
				if part.Type == "text" {
					contentText += part.Text
				}
			}
		}
		utils.L().Debugw("Message details", "index", i, "role", msg.Role, "content_length", len(contentText), "has_multicontent", len(msg.MultiContent) > 0, "cew", truncate(contentText, 100))
	}

	// Validate that there's at least one non-empty user message
	hasUserMessage := false
	for _, msg := range req.Messages {
		if msg.Role == openai.ChatMessageRoleUser {
			// Check both Content and MultiContent
			if msg.Content != "" {
				hasUserMessage = true
				break
			}
			// Check MultiContent for text
			for _, part := range msg.MultiContent {
				if part.Type == "text" && part.Text != "" {
					hasUserMessage = true
					break
				}
			}
			if hasUserMessage {
				break
			}
		}
	}
	if !hasUserMessage {
		utils.L().Errorf("Request has no user message content")
		http.Error(w, "request must contain at least one non-empty user message", http.StatusBadRequest)
		return
	}

	// Create router request
	routerReq := &router.Request{
		Protocol: provider.ProtocolOpenAI,
		Model:    req.Model,
		Payload:  &req,
		Headers:  make(map[string]string),
		IsStream: req.Stream,
	}

	// Copy relevant headers
	for key, values := range r.Header {
		if len(values) > 0 {
			routerReq.Headers[key] = values[0]
		}
	}

	// Execute using SmartRouter
	resp, err := s.sr.Execute(r.Context(), routerReq)
	if err != nil {
		utils.L().Errorf("SmartRouter execution failed: %v", err)
		s.writeErrorResponse(w, err)
		return
	}

	// Copy response headers
	for key, value := range resp.Headers {
		w.Header().Set(key, value)
	}

	// Set status code
	w.WriteHeader(resp.StatusCode)

	// Stream the response body
	if _, err := io.Copy(w, resp.Body); err != nil {
		utils.L().Errorf("Failed to write response body: %v", err)
	}
	resp.Body.Close()
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func (s *Server) HandleProviderChat(w http.ResponseWriter, r *http.Request) {
	providerType := chi.URLParam(r, "provider")

	// Only allow qwen and iflow providers for OpenAI-compatible API
	pType := provider.ProviderType(providerType)
	if pType != provider.ProviderQwen && pType != provider.ProviderIFlow {
		utils.L().Warnf("Provider %s not available for OpenAI-compatible API", providerType)
		http.Error(w, fmt.Sprintf("provider '%s' is not available for OpenAI-compatible API. Available providers: qwen, iflow", providerType), http.StatusNotFound)
		return
	}

	var req openai.ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Create router request with provider preference
	routerReq := &router.Request{
		Protocol: provider.ProtocolOpenAI,
		Model:    req.Model,
		Payload:  &req,
		Headers:  make(map[string]string),
		IsStream: req.Stream,
	}

	// Add provider preference in headers for SmartRouter to use
	routerReq.Headers["X-Preferred-Provider"] = providerType

	// Copy relevant headers
	for key, values := range r.Header {
		if len(values) > 0 {
			routerReq.Headers[key] = values[0]
		}
	}

	// Execute using SmartRouter
	resp, err := s.sr.Execute(r.Context(), routerReq)
	if err != nil {
		utils.L().Errorf("SmartRouter execution failed: %v", err)
		s.writeErrorResponse(w, err)
		return
	}

	// Copy response headers
	for key, value := range resp.Headers {
		w.Header().Set(key, value)
	}

	// Set status code
	w.WriteHeader(resp.StatusCode)

	// Stream the response body
	if _, err := io.Copy(w, resp.Body); err != nil {
		utils.L().Errorf("Failed to write response body: %v", err)
	}
	resp.Body.Close()
}

// writeErrorResponse writes an error response with proper status code
func (s *Server) writeErrorResponse(w http.ResponseWriter, err error) {
	// Check if it's a ProviderError with status code
	if provErr, ok := err.(*provider.ProviderError); ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(provErr.StatusCode)

		// Format error response similar to OpenAI API
		errorResp := map[string]interface{}{
			"error": map[string]interface{}{
				"message": provErr.Message,
				"type":    "provider_error",
				"code":    provErr.StatusCode,
			},
		}

		if provErr.Details != nil {
			errorResp["error"].(map[string]interface{})["details"] = provErr.Details
		}

		if err := json.NewEncoder(w).Encode(errorResp); err != nil {
			utils.L().Errorf("Failed to encode error response: %v", err)
		}
		return
	}

	// Fallback to generic 500 e for non-ProviderError
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

