package antigravity

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	cloudauth "cloud.google.com/go/auth"
	"github.com/sunbankio/omniproxy/internal/provider"
	"github.com/sunbankio/omniproxy/pkg/utils"
	"google.golang.org/genai"
)

const (
	AntigravityBaseURL = "https://daily-cloudcode-pa.sandbox.googleapis.com"
	APIVersion         = "v1internal"
	DefaultUserAgent   = "antigravity/1.11.5 windows/amd64"
)

type AntigravityProvider struct {
	client       *genai.Client
	name         string
	auth         *Authenticator
	cachedModels []string
	mu           sync.RWMutex
	modelsCached bool
}

// createGenAIClient creates a new genai.Client with the given project ID
func (p *AntigravityProvider) createGenAIClient(ctx context.Context, projectID string) (*genai.Client, error) {
	tokenProvider := &TokenProvider{authenticator: p.auth}
	creds := cloudauth.NewCredentials(&cloudauth.CredentialsOptions{
		TokenProvider: tokenProvider,
	})

	return genai.NewClient(ctx, &genai.ClientConfig{
		Backend:     genai.BackendAntigravity,
		Project:     projectID,
		Credentials: creds,
		HTTPOptions: genai.HTTPOptions{
			BaseURL: AntigravityBaseURL,
		},
	})
}

// refreshModelCache fetches and caches available models
func (p *AntigravityProvider) refreshModelCache(ctx context.Context) {
	models, err := p.ListModels(ctx)
	if err == nil {
		p.mu.Lock()
		p.cachedModels = models
		p.modelsCached = true
		p.mu.Unlock()
		utils.L().Infow("Cached available models for provider",
			"provider", p.name,
			"models", models)
	} else {
		utils.L().Warnw("Failed to fetch models for provider",
			"provider", p.name,
			"error", err)
	}
}

// NewProvider creates a new Antigravity provider with auth
func NewProvider(ctx context.Context, name string, auth *Authenticator) (*AntigravityProvider, error) {
	// 1. Discover Project ID using the official Google library method (same as POC)
	tokenProvider := &TokenProvider{authenticator: auth}
	creds := cloudauth.NewCredentials(&cloudauth.CredentialsOptions{
		TokenProvider: tokenProvider,
	})

	projectID, err := genai.DiscoverCloudCodeProject(ctx, creds, genai.BackendAntigravity)
	if err != nil {
		// Fallback as seen in POC
		projectID = "substantial-dragon-7kd70"
	}

	provider := &AntigravityProvider{
		name: name,
		auth: auth,
	}

	// 2. Create GenAI Client
	client, err := provider.createGenAIClient(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to create genai client: %w", err)
	}
	provider.client = client

	// Fetch and cache available models at startup
	provider.refreshModelCache(ctx)

	return provider, nil
}

// NewProviderWithGeminiAuth creates a new Antigravity provider using Authenticator with project ID support
func NewProviderWithGeminiAuth(ctx context.Context, name string, auth *Authenticator) (*AntigravityProvider, error) {
	// 1. Check if project ID is already stored in credentials
	projectID := auth.GetProjectID()

	if projectID == "" {
		// Project ID not stored, need to discover it
		utils.L().Infow("Project ID not found in credentials, discovering...",
			"provider", name,
			"creds_path", auth.GetCredentialsPath())

		// Use the official Google library method for project discovery (same as POC)
		tokenProvider := &TokenProvider{authenticator: auth}
		creds := cloudauth.NewCredentials(&cloudauth.CredentialsOptions{
			TokenProvider: tokenProvider,
		})

		discoveredID, err := genai.DiscoverCloudCodeProject(ctx, creds, genai.BackendAntigravity)
		if err != nil {
			// Fallback as seen in POC
			utils.L().Warnw("Failed to discover project ID, using fallback",
				"provider", name,
				"error", err,
				"fallback_project", "substantial-dragon-7kd70")
			projectID = "substantial-dragon-7kd70"
		} else {
			projectID = discoveredID
			utils.L().Infow("Successfully discovered project ID",
				"provider", name,
				"project_id", projectID)

			// Save the discovered project ID to credentials for future use
			if err := auth.SetProjectID(ctx, projectID); err != nil {
				utils.L().Warnw("Failed to save project ID to credentials",
					"provider", name,
					"error", err)
			}
		}
	} else {
		utils.L().Infow("Using stored project ID from credentials",
			"provider", name,
			"project_id", projectID,
			"creds_path", auth.GetCredentialsPath())
	}

	provider := &AntigravityProvider{
		name: name,
		auth: auth,
	}

	// 2. Create GenAI Client using the same token provider as POC
	client, err := provider.createGenAIClient(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to create genai client: %w", err)
	}
	provider.client = client

	utils.L().Infow("Successfully created Antigravity provider",
		"provider", name,
		"project_id", projectID)

	// Fetch and cache available models at startup
	provider.refreshModelCache(ctx)

	return provider, nil
}

func (p *AntigravityProvider) Type() provider.ProviderType {
	return provider.ProviderAntigravity
}

func (p *AntigravityProvider) Name() string {
	return p.name
}

func (p *AntigravityProvider) SupportedProtocols() []provider.Protocol {
	return []provider.Protocol{provider.ProtocolGemini}
}

func (p *AntigravityProvider) GetClient() *genai.Client {
	return p.client
}

func (p *AntigravityProvider) GetAuth() *Authenticator {
	return p.auth
}

// RefreshClient recreates the genai.Client with fresh credentials after token refresh
// This is necessary because genai.Client caches tokens internally and doesn't automatically pick up refreshed tokens
func (p *AntigravityProvider) RefreshClient(ctx context.Context) (*genai.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Get Project ID (use stored or discover)
	projectID := p.auth.GetProjectID()
	if projectID == "" {
		// Use the official Google library method for project discovery (same as POC)
		tokenProvider := &TokenProvider{authenticator: p.auth}
		creds := cloudauth.NewCredentials(&cloudauth.CredentialsOptions{
			TokenProvider: tokenProvider,
		})

		discoveredID, err := genai.DiscoverCloudCodeProject(ctx, creds, genai.BackendAntigravity)
		if err != nil {
			// Fallback as seen in POC
			utils.L().Warnw("Failed to discover project ID during client refresh, using fallback",
				"provider", p.name,
				"error", err)
			projectID = "substantial-dragon-7kd70"
		} else {
			projectID = discoveredID
		}
	}

	// Create new genai.Client with fresh credentials
	client, err := p.createGenAIClient(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to create new genai client during refresh: %w", err)
	}

	// Update the client reference
	p.client = client

	utils.L().Infow("Successfully refreshed genai client",
		"provider", p.name,
		"project_id", projectID)

	return p.client, nil
}

func (p *AntigravityProvider) ListModels(ctx context.Context) ([]string, error) {
	p.mu.RLock()
	if len(p.cachedModels) > 0 {
		defer p.mu.RUnlock()
		return p.cachedModels, nil
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()

	// Double check after acquiring write lock
	if len(p.cachedModels) > 0 {
		return p.cachedModels, nil
	}

	fallbackModels := []string{
		"gemini-2.5-computer-use-preview-10-2025",
		"gemini-3-pro-image-preview",
		"gemini-3-pro-preview",
		"gemini-3-flash",
		"gemini-2.5-flash",
		"claude-sonnet-4-5",
		"claude-sonnet-4-5-thinking",
		"claude-opus-4-5-thinking",
	}

	var token string
	var err error

	if p.auth != nil {
		token, err = p.auth.GetToken(ctx)
	} else {
		utils.L().Warnf("No authenticator available for Antigravity models")
		p.cachedModels = fallbackModels
		return fallbackModels, nil
	}

	if err != nil {
		utils.L().Warnf("Failed to get token for Antigravity models: %v", err)
		p.cachedModels = fallbackModels
		return fallbackModels, nil
	}

	url := fmt.Sprintf("%s/%s:fetchAvailableModels", AntigravityBaseURL, APIVersion)
	req, err := http.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		p.cachedModels = fallbackModels
		return fallbackModels, nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", DefaultUserAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		utils.L().Warnf("Error calling fetchAvailableModels: %v", err)
		p.cachedModels = fallbackModels
		return fallbackModels, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		utils.L().Warnf("fetchAvailableModels failed (%d): %s", resp.StatusCode, string(body))
		p.cachedModels = fallbackModels
		return fallbackModels, nil
	}

	var rawResponse map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&rawResponse); err != nil {
		utils.L().Warnf("Failed to decode Antigravity models: %v", err)
		p.cachedModels = fallbackModels
		return fallbackModels, nil
	}

	var models []string
	if modelsData, exists := rawResponse["models"]; exists {
		if modelsMap, ok := modelsData.(map[string]interface{}); ok {
			for modelID := range modelsMap {
				name := strings.TrimPrefix(modelID, "models/")
				models = append(models, name)
			}
		}
	}

	if len(models) > 0 {
		p.cachedModels = models
		return models, nil
	}

	p.cachedModels = fallbackModels
	return fallbackModels, nil
}

func (p *AntigravityProvider) SupportsModel(model string) bool {
	p.mu.RLock()
	cached := p.cachedModels
	p.mu.RUnlock()

	for _, supported := range cached {
		if supported == model {
			return true
		}
	}
	return false
}
