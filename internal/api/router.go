package api

import (
	"encoding/json"
	"fmt"
	"net/http"
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
	var req openai.ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	p, selectionReason, err := s.pm.GetProviderByModelWithReason(req.Model)
	if err != nil {
		utils.L().Errorf("Failed to find provider for model %s: %v", req.Model, err)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// Log provider selection with detailed reasoning
	utils.L().Infof("Model '%s' -> Provider: %s (type: %s) - Reason: %s", req.Model, p.Name(), p.Type(), selectionReason)

	s.executeChat(w, r, p, req)
}

func (s *Server) HandleProviderChat(w http.ResponseWriter, r *http.Request) {
	providerType := chi.URLParam(r, "provider")
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.pm.RecordSuccess(req.Model, p)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		utils.L().Errorf("Failed to encode response: %v", err)
	}
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
