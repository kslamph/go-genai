package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/sunbankio/omniproxy/internal/auth"
	"github.com/sunbankio/omniproxy/internal/manager"
	"github.com/sunbankio/omniproxy/internal/router"
	"github.com/sunbankio/omniproxy/pkg/utils"
)

type Server struct {
	ps      *manager.ProviderService
	sr      *router.SmartRouter
	authMgr auth.AuthManager
	router  *chi.Mux
}

func NewServer(ps *manager.ProviderService, authMgr auth.AuthManager) *Server {
	// Initialize SmartRouter with dependencies
	sr := router.NewSmartRouter(authMgr, ps.GetRegistry(), ps.GetLoadBalancer())

	s := &Server{
		ps:      ps,
		sr:      sr,
		authMgr: authMgr,
		router:  chi.NewRouter(),
	}
	s.setupRoutes()
	return s
}

// logProviderRequest logs the start of a provider request in a unified format
func logProviderRequest(providerName, providerType, model, path, userAgent string, isStream bool) {
	utils.L().Infow("PROCESSING",
		"provider", providerName,
		"provider_type", providerType,
		"model", model,
		"stream", isStream,
		"path", path,
		"user_agent", userAgent)
}

// logProviderError logs a provider error in a unified format
func logProviderError(providerName, providerType, model string, isStream bool, err error) {
	utils.L().Errorw("Provider request failed",
		"provider", providerName,
		"provider_type", providerType,
		"model", model,
		"stream", isStream,
		"error", err)
}

func (s *Server) setupRoutes() {
	s.router.Use(middleware.RequestID)
	s.router.Use(middleware.RealIP)
	s.router.Use(middleware.Logger) // Chi's default logger
	s.router.Use(middleware.Recoverer)

	// OpenAI-compatible endpoints (qwen, iflow)
	s.router.Route("/v1", func(r chi.Router) {
		r.Post("/chat/completions", s.HandleChat)
		r.Get("/models", s.HandleListModels)
	})

	s.router.Route("/{provider}/v1", func(r chi.Router) {
		r.Post("/chat/completions", s.HandleProviderChat)
		r.Get("/models", s.HandleProviderListModels)
	})

	// Gemini v1beta API endpoints (gemini, antigravity)
	// Following: https://ai.google.dev/api/all-methods

	// All Gemini providers (gemini + antigravity) - load balanced
	// Using unified handlers for both specific and load-balanced routes
	s.router.Route("/v1beta", func(r chi.Router) {
		// List models: GET /v1beta/models
		r.Get("/models", s.HandleGeminiUnifiedModels)

		// Generate content: POST /v1beta/models/{model}:generateContent
		r.Post("/models/{model}:generateContent", s.HandleGeminiUnifiedGenerateContent)

		// Stream generate content: POST /v1beta/models/{model}:streamGenerateContent
		r.Post("/models/{model}:streamGenerateContent", s.HandleGeminiUnifiedStreamGenerateContent)
	})

	// Specific provider Gemini v1beta API endpoints
	s.router.Route("/{provider}/v1beta", func(r chi.Router) {
		// List models: GET /{provider}/v1beta/models
		r.Get("/models", s.HandleGeminiUnifiedModels)

		// Generate content: POST /{provider}/v1beta/{model}:generateContent
		r.Post("/{model}:generateContent", s.HandleGeminiUnifiedGenerateContent)

		// Stream generate content: POST /{provider}/v1beta/{model}:streamGenerateContent
		r.Post("/{model}:streamGenerateContent", s.HandleGeminiUnifiedStreamGenerateContent)
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
	models, err := s.ps.ListModels(r.Context())
	if err != nil {
		utils.L().Errorf("Failed to list models: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeModelsResponse(w, models)
}
