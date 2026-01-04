package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/sunbankio/omniproxy/internal/config"
	"github.com/sunbankio/omniproxy/internal/manager"
	"github.com/sunbankio/omniproxy/pkg/utils"
)

// APIResponse is a generic wrapper for API responses.
type APIResponse struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
}

// AdminHandler handles the admin API endpoints.
type AdminHandler struct {
	registry *manager.Registry
	config   *config.Config
}

// NewAdminHandler creates a new AdminHandler.
func NewAdminHandler(registry *manager.Registry, cfg *config.Config) *AdminHandler {
	return &AdminHandler{
		registry: registry,
		config:   cfg,
	}
}

// HandleListCredentials handles GET /admin/api/credentials
func (h *AdminHandler) HandleListCredentials(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if r := recover(); r != nil {
			utils.L().Errorf("AdminHandler: Panic in HandleListCredentials: %v", r)
			http.Error(w, "An internal error occurred", http.StatusInternalServerError)
		}
	}()

	statuses := h.registry.GetAllCredentialsStatus()
	response := APIResponse{
		Success: true,
		Data:    statuses,
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		utils.L().Errorf("AdminHandler: Failed to encode credentials response: %v", err)
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}

// HandleResetPenalty handles POST /admin/api/credentials/{id}/reset
func (h *AdminHandler) HandleResetPenalty(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if r := recover(); r != nil {
			utils.L().Errorf("AdminHandler: Panic in HandleResetPenalty: %v", r)
			http.Error(w, "An internal error occurred", http.StatusInternalServerError)
		}
	}()

	credentialID := chi.URLParam(r, "id")
	if credentialID == "" {
		http.Error(w, "Credential ID is required", http.StatusBadRequest)
		return
	}

	err := h.registry.ResetCredentialPenalty(credentialID)
	if err != nil {
		response := APIResponse{
			Success: false,
			Error:   err.Error(),
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound) // 404 is appropriate for a resource not found
		json.NewEncoder(w).Encode(response)
		return
	}

	response := APIResponse{
		Success: true,
		Data:    map[string]string{"message": "Penalty reset successfully"},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// HandleGetConfig handles GET /admin/api/config
func (h *AdminHandler) HandleGetConfig(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if r := recover(); r != nil {
			utils.L().Errorf("AdminHandler: Panic in HandleGetConfig: %v", r)
			http.Error(w, "An internal error occurred", http.StatusInternalServerError)
		}
	}()

	response := APIResponse{
		Success: true,
		Data:    h.config,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// HandleGetModels handles GET /admin/api/models
func (h *AdminHandler) HandleGetModels(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if r := recover(); r != nil {
			utils.L().Errorf("AdminHandler: Panic in HandleGetModels: %v", r)
			http.Error(w, "An internal error occurred", http.StatusInternalServerError)
		}
	}()

	modelsByProvider := h.registry.GetModelsByProvider()
	response := APIResponse{
		Success: true,
		Data:    modelsByProvider,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}