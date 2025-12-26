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
	"github.com/go-chi/chi/v5/middleware"
	"github.com/sashabaranov/go-openai"
	"github.com/sunbankio/omniproxy/internal/manager"
	"github.com/sunbankio/omniproxy/internal/provider"
	"github.com/sunbankio/omniproxy/pkg/utils"
)

type Server struct {
	pm     *manager.PoolManager
	router *chi.Mux
}

func NewServer(pm *manager.PoolManager) *Server {
	s := &Server{
		pm:     pm,
		router: chi.NewRouter(),
	}
	s.setupRoutes()
	return s
}

func (s *Server) setupRoutes() {
	s.router.Use(middleware.RequestID)
	s.router.Use(middleware.RealIP)
	s.router.Use(middleware.Logger) // Chi's default logger
	s.router.Use(middleware.Recoverer)

	s.router.Route("/v1", func(r chi.Router) {
		r.Post("/chat/completions", s.HandleChat)
		r.Get("/models", s.HandleListModels)
	})

	s.router.Route("/{provider}/v1", func(r chi.Router) {
		r.Post("/chat/completions", s.HandleProviderChat)
		r.Get("/models", s.HandleProviderListModels)
	})

	s.router.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

func (s *Server) HandleListModels(w http.ResponseWriter, r *http.Request) {
	models, err := s.pm.ListModels(r.Context())
	if err != nil {
		utils.L().Errorf("Failed to list models: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeModelsResponse(w, models)
}

func (s *Server) HandleProviderListModels(w http.ResponseWriter, r *http.Request) {
	providerType := chi.URLParam(r, "provider")

	// Only allow qwen and iflow providers for OpenAI-compatible API
	if providerType != "qwen" && providerType != "iflow" {
		utils.L().Warnf("Provider %s not available for OpenAI-compatible API", providerType)
		http.Error(w, fmt.Sprintf("provider '%s' is not available for OpenAI-compatible API. Available providers: qwen, iflow", providerType), http.StatusNotFound)
		return
	}

	models, err := s.pm.ListProviderModels(r.Context(), providerType)
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

	p, selectionReason, err := s.pm.GetProviderByModelForOpenAI(req.Model)
	if err != nil {
		utils.L().Errorf("Failed to find OpenAI-compatible provider for model %s: %v", req.Model, err)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// Log provider selection with detailed reasoning
	utils.L().Infof("Model '%s' -> Provider: %s (type: %s) - Reason: %s", req.Model, p.Name(), p.Type(), selectionReason)

	s.executeChat(w, r, p, req)
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
	if providerType != "qwen" && providerType != "iflow" {
		utils.L().Warnf("Provider %s not available for OpenAI-compatible API", providerType)
		http.Error(w, fmt.Sprintf("provider '%s' is not available for OpenAI-compatible API. Available providers: qwen, iflow", providerType), http.StatusNotFound)
		return
	}

	var req openai.ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	p, selectionReason, err := s.pm.GetProviderWithReason(providerType, req.Model)
	if err != nil {
		utils.L().Errorf("Failed to find provider %s for model %s: %v", providerType, req.Model, err)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// Log provider selection with detailed reasoning
	utils.L().Infof("Model '%s' -> Provider: %s (type: %s) - Reason: %s (explicit provider: %s)", req.Model, p.Name(), p.Type(), selectionReason, providerType)

	s.executeChat(w, r, p, req)
}

func (s *Server) executeChat(w http.ResponseWriter, r *http.Request, p provider.Provider, req openai.ChatCompletionRequest) {
	if req.Stream {
		s.streamChat(w, r, p, req)
	} else {
		s.normalChat(w, r, p, req)
	}
}

func (s *Server) normalChat(w http.ResponseWriter, r *http.Request, p provider.Provider, req openai.ChatCompletionRequest) {
	resp, err := p.ChatCompletion(r.Context(), req)
	if err != nil {
		utils.L().Errorf("Provider %s failed: %v", p.Name(), err)
		s.pm.RecordFailure(p) // Record failure for load balancing
		s.writeErrorResponse(w, err)
		return
	}

	s.pm.RecordSuccess(req.Model, p)

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

func (s *Server) streamChat(w http.ResponseWriter, r *http.Request, p provider.Provider, req openai.ChatCompletionRequest) {
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
				utils.L().Errorf("Stream error from provider %s: %v", p.Name(), err)
				s.pm.RecordFailure(p) // Record failure for load balancing
				// SSE error handling is tricky, often just closing is best if started
			}
			return
		case resp, ok := <-respChan:
			if !ok {
				// Success record after stream finishes successfully
				s.pm.RecordSuccess(req.Model, p)
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
