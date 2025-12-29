package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	cloudauth "cloud.google.com/go/auth"
	"github.com/sunbankio/omniproxy/auth"
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
	geminiAuth   *auth.GeminiAuthenticator
	cachedModels []string
	mu           sync.RWMutex
	modelsCached bool
}

// NewProvider creates a new Antigravity provider with auth
func NewProvider(ctx context.Context, name string, auth *Authenticator) (*AntigravityProvider, error) {
	// 1. Discover Project ID
	projectID, err := discoverProjectID(ctx, auth, AntigravityBaseURL)
	if err != nil {
		// Fallback as seen in POC
		projectID = "antigravity-test-project"
	}

	// 2. Create GenAI Client
	tokenProvider := &TokenProvider{authenticator: auth}
	creds := cloudauth.NewCredentials(&cloudauth.CredentialsOptions{
		TokenProvider: tokenProvider,
	})

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:     genai.BackendAntigravity,
		Project:     projectID,
		Credentials: creds,
		HTTPOptions: genai.HTTPOptions{
			BaseURL: AntigravityBaseURL,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create genai client: %w", err)
	}

	provider := &AntigravityProvider{
		client: client,
		name:   name,
		auth:   auth,
	}

	// Fetch and cache available models at startup
	models, err := provider.ListModels(ctx)
	if err == nil {
		provider.mu.Lock()
		provider.cachedModels = models
		provider.modelsCached = true
		provider.mu.Unlock()
		utils.L().Infow("Cached available models for provider at startup",
			"provider", name,
			"project_id", projectID,
			"models", models)
	} else {
		utils.L().Warnw("Failed to fetch models for provider at startup",
			"provider", name,
			"project_id", projectID,
			"error", err)
	}

	return provider, nil
}

// NewProviderWithGeminiAuth creates a new Antigravity provider using auth.GeminiAuthenticator (like POC)
func NewProviderWithGeminiAuth(ctx context.Context, name string, geminiAuth *auth.GeminiAuthenticator) (*AntigravityProvider, error) {
	// 1. Check if project ID is already stored in credentials
	projectID := geminiAuth.GetProjectID()

	if projectID == "" {
		// Project ID not stored, need to discover it
		utils.L().Infow("Project ID not found in credentials, discovering...",
			"provider", name,
			"creds_path", geminiAuth.GetCredentialsPath())

		discoveredID, err := discoverProjectIDWithGeminiAuth(ctx, geminiAuth, AntigravityBaseURL)
		if err != nil {
			// Fallback as seen in POC
			utils.L().Warnw("Failed to discover project ID, using fallback",
				"provider", name,
				"error", err,
				"fallback_project", "antigravity-test-project")
			projectID = "antigravity-test-project"
		} else {
			projectID = discoveredID
			utils.L().Infow("Successfully discovered project ID",
				"provider", name,
				"project_id", projectID)

			// Save the discovered project ID to credentials for future use
			if err := geminiAuth.SetProjectID(ctx, projectID); err != nil {
				utils.L().Warnw("Failed to save project ID to credentials",
					"provider", name,
					"error", err)
			}
		}
	} else {
		utils.L().Infow("Using stored project ID from credentials",
			"provider", name,
			"project_id", projectID,
			"creds_path", geminiAuth.GetCredentialsPath())
	}

	// 2. Create GenAI Client using the same token provider as POC
	tokenProvider := &geminiTokenProvider{authenticator: geminiAuth}
	creds := cloudauth.NewCredentials(&cloudauth.CredentialsOptions{
		TokenProvider: tokenProvider,
	})

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:     genai.BackendAntigravity,
		Project:     projectID,
		Credentials: creds,
		HTTPOptions: genai.HTTPOptions{
			BaseURL: AntigravityBaseURL,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create genai client: %w", err)
	}

	utils.L().Infow("Successfully created Antigravity provider",
		"provider", name,
		"project_id", projectID)

	provider := &AntigravityProvider{
		client:     client,
		name:       name,
		auth:       nil,
		geminiAuth: geminiAuth,
	}

	// Fetch and cache available models at startup
	models, err := provider.ListModels(ctx)
	if err == nil {
		provider.mu.Lock()
		provider.cachedModels = models
		provider.modelsCached = true
		provider.mu.Unlock()
		utils.L().Infow("Cached available models for provider at startup",
			"provider", name,
			"project_id", projectID,
			"models", models)
	} else {
		utils.L().Warnw("Failed to fetch models for provider at startup",
			"provider", name,
			"project_id", projectID,
			"error", err)
	}

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

func (p *AntigravityProvider) GetGeminiAuth() *auth.GeminiAuthenticator {
	return p.geminiAuth
}

func (p *AntigravityProvider) getProjectID() string {
	if p.geminiAuth != nil {
		return p.geminiAuth.GetProjectID()
	}
	return "unknown"
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
	} else if p.geminiAuth != nil {
		token, err = p.geminiAuth.GetToken(ctx)
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

type geminiTokenProvider struct {
	authenticator *auth.GeminiAuthenticator
}

func (p *geminiTokenProvider) Token(ctx context.Context) (*cloudauth.Token, error) {
	token, err := p.authenticator.GetToken(ctx)
	if err != nil {
		utils.L().Errorw("Failed to get token from authenticator",
			"creds_path", p.authenticator.GetCredentialsPath(),
			"error", err)
		return nil, err
	}

	tokenPreview := token
	if len(token) > 20 {
		tokenPreview = token[:20] + "..."
	}
	utils.L().Debugw("Token retrieved",
		"creds_path", p.authenticator.GetCredentialsPath(),
		"token_preview", tokenPreview)

	return &cloudauth.Token{
		Value:  token,
		Expiry: time.Now().Add(time.Hour),
	}, nil
}

func discoverProjectIDWithGeminiAuth(ctx context.Context, authenticator *auth.GeminiAuthenticator, baseURL string) (string, error) {
	authenticator.ForceRefresh(ctx)

	for attempt := 0; attempt < 2; attempt++ {
		token, err := authenticator.GetToken(ctx)
		if err != nil {
			return "", err
		}

		clientMetadata := map[string]interface{}{
			"ideType":    "IDE_UNSPECIFIED",
			"platform":   "PLATFORM_UNSPECIFIED",
			"pluginType": "GEMINI",
		}

		loadRequest := map[string]interface{}{
			"cloudaicompanionProject": "",
			"metadata":                clientMetadata,
		}

		reqBody, _ := json.Marshal(loadRequest)
		url := fmt.Sprintf("%s/v1internal:loadCodeAssist", baseURL)
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody))
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", err
		}

		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			_ = resp.Body.Close()
			if err := authenticator.ForceRefresh(ctx); err != nil {
				return "", fmt.Errorf("failed to force refresh token: %w", err)
			}
			continue
		}

		defer func() {
			_ = resp.Body.Close()
		}()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return "", fmt.Errorf("loadCodeAssist failed (%d): %s", resp.StatusCode, string(body))
		}

		var loadResponse map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&loadResponse); err != nil {
			return "", err
		}

		if projectID, ok := loadResponse["cloudaicompanionProject"].(string); ok && projectID != "" {
			return projectID, nil
		}

		return "", fmt.Errorf("failed to discover project ID: response missing project ID")
	}
	return "", fmt.Errorf("failed to discover project ID after retries")
}

func discoverProjectID(ctx context.Context, authenticator *Authenticator, baseURL string) (string, error) {
	for attempt := 0; attempt < 2; attempt++ {
		token, err := authenticator.GetToken(ctx)
		if err != nil {
			return "", err
		}

		clientMetadata := map[string]interface{}{
			"ideType":    "IDE_UNSPECIFIED",
			"platform":   "PLATFORM_UNSPECIFIED",
			"pluginType": "GEMINI",
		}

		loadRequest := map[string]interface{}{
			"cloudaicompanionProject": "",
			"metadata":                clientMetadata,
		}

		reqBody, _ := json.Marshal(loadRequest)
		url := fmt.Sprintf("%s/v1internal:loadCodeAssist", baseURL)
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody))
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", err
		}

		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			_ = resp.Body.Close()
			if err := authenticator.ForceRefresh(ctx); err != nil {
				return "", fmt.Errorf("failed to force refresh token: %w", err)
			}
			continue
		}

		defer func() {
			_ = resp.Body.Close()
		}()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return "", fmt.Errorf("loadCodeAssist failed (%d): %s", resp.StatusCode, string(body))
		}

		var loadResponse map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&loadResponse); err != nil {
			return "", err
		}

		if projectID, ok := loadResponse["cloudaicompanionProject"].(string); ok && projectID != "" {
			return projectID, nil
		}

		return "", fmt.Errorf("failed to discover project ID: response missing project ID")
	}
	return "", fmt.Errorf("failed to discover project ID after retries")
}
