package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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
	// Log incoming request details to help debug duplicate requests
	utils.L().Infow("Incoming chat completion request",
		"method", r.Method,
		"path", r.URL.Path,
		"query", r.URL.RawQuery,
		"content_length", r.ContentLength,
		"remote_addr", r.RemoteAddr,
		"user_agent", r.UserAgent())

	// Read the raw body for logging
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	// Log the raw request body (debug only)
	utils.L().Debugw("Raw HTTP request body", "body", string(bodyBytes))

	// Save raw request to file for inspection (debug mode only)
	if utils.IsDebugMode() {
		timestamp := time.Now().Format("20060102-150405.000")
		filename := fmt.Sprintf("request-%s.json", timestamp)
		if err := os.WriteFile(filename, bodyBytes, 0644); err != nil {
			utils.L().Warnw("Failed to write request to file", "error", err, "filename", filename)
		} else {
			utils.L().Infow("Saved request to file", "filename", filename)
		}
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

func (s *Server) executeChat(w http.ResponseWriter, r *http.Request, p provider.OpenAICompatibleProvider, req openai.ChatCompletionRequest) {
	if req.Stream {
		s.streamChat(w, r, p, req)
	} else {
		s.normalChat(w, r, p, req)
	}
}

func (s *Server) normalChat(w http.ResponseWriter, r *http.Request, p provider.OpenAICompatibleProvider, req openai.ChatCompletionRequest) {
	logProviderRequest(p.Name(), string(p.Type()), req.Model, r.URL.Path, r.UserAgent(), false)

	resp, err := p.ChatCompletion(r.Context(), req)
	if err != nil {
		logProviderError(p.Name(), string(p.Type()), req.Model, false, err)
		s.ps.RecordFailure(p) // Record failure for load balancing
		s.writeErrorResponse(w, err)
		return
	}

	s.ps.RecordSuccess(req.Model, p)

	// Save response to file for inspection (debug mode only)
	if utils.IsDebugMode() {
		respBytes, err := json.MarshalIndent(resp, "", "  ")
		if err == nil {
			timestamp := time.Now().Format("20060102-150405.000")
			filename := fmt.Sprintf("response-%s.json", timestamp)
			if err := os.WriteFile(filename, respBytes, 0644); err != nil {
				utils.L().Warnw("Failed to write response to file", "error", err, "filename", filename)
			} else {
				utils.L().Infow("Saved response to file", "filename", filename)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		utils.L().Errorf("Failed to encode response: %v", err)
	}
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

func (s *Server) streamChat(w http.ResponseWriter, r *http.Request, p provider.OpenAICompatibleProvider, req openai.ChatCompletionRequest) {
	logProviderRequest(p.Name(), string(p.Type()), req.Model, r.URL.Path, r.UserAgent(), true)

	respChan, errChan := p.StreamChatCompletion(r.Context(), req)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Transfer-Encoding", "chunked")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case err := <-errChan:
			if err != nil {
				logProviderError(p.Name(), string(p.Type()), req.Model, true, err)
				s.ps.RecordFailure(p) // Record failure for load balancing
				// SSE error handling is tricky, often just closing is best if started
			}
			return
		case resp, ok := <-respChan:
			if !ok {
				// Success record after stream finishes successfully
				s.ps.RecordSuccess(req.Model, p)
				_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
				flusher.Flush()
				return
			}
			data, _ := json.Marshal(resp)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", string(data))
			flusher.Flush()
		}
	}
}
