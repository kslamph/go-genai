package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	cloudauth "cloud.google.com/go/auth"
	"github.com/sashabaranov/go-openai"
	"github.com/sunbankio/omniproxy/auth"
	"github.com/sunbankio/omniproxy/internal/provider"
	"github.com/sunbankio/omniproxy/internal/provider/gemini"
	"github.com/sunbankio/omniproxy/pkg/utils"
	"google.golang.org/genai"
)

const (
	AntigravityBaseURL = "https://daily-cloudcode-pa.sandbox.googleapis.com"
	APIVersion         = "v1internal"
	DefaultUserAgent   = "antigravity/1.11.5 windows/amd64"
)

type AntigravityProvider struct {
	client     *genai.Client
	name       string
	auth       *Authenticator
	geminiAuth *auth.GeminiAuthenticator
}

// Ensure AntigravityProvider implements provider.Provider
var _ provider.Provider = (*AntigravityProvider)(nil)

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

	return &AntigravityProvider{
		client: client,
		name:   name,
		auth:   auth,
	}, nil
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

	// Log available models for this provider
	provider := &AntigravityProvider{
		client:     client,
		name:       name,
		auth:       nil,
		geminiAuth: geminiAuth,
	}

	// Fetch and log available models
	models, err := provider.ListModels(ctx)
	if err == nil {
		utils.L().Infow("Available models for provider",
			"provider", name,
			"project_id", projectID,
			"models", models)
	} else {
		utils.L().Warnw("Failed to fetch models for provider",
			"provider", name,
			"project_id", projectID,
			"error", err)
	}

	return provider, nil
}

func (p *AntigravityProvider) Type() string {
	return "antigravity"
}

func (p *AntigravityProvider) Name() string {
	return p.name
}

func (p *AntigravityProvider) ChatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (*openai.ChatCompletionResponse, error) {
	sys, contents, config, err := gemini.ToGeminiRequest(req)
	if err != nil {
		utils.L().Errorw("Failed to convert request to Gemini format",
			"provider", p.name,
			"error", err)
		return nil, err
	}

	if sys != nil {
		config.SystemInstruction = sys
	}

	utils.L().Infow("Calling GenerateContent",
		"provider", p.name,
		"model", req.Model,
		"num_contents", len(contents),
		"has_system", sys != nil,
		"project_id", p.getProjectID(),
		"base_url", AntigravityBaseURL,
		"temperature", config.Temperature,
		"max_tokens", config.MaxOutputTokens)

	// Debug: Print full request details
	utils.L().Debugw("Full request details",
		"provider", p.name,
		"original_model", req.Model,
		"num_messages", len(req.Messages),
		"stream", req.Stream)

	resp, err := p.client.Models.GenerateContent(ctx, req.Model, contents, config)
	if err != nil {
		utils.L().Errorw("GenerateContent failed",
			"provider", p.name,
			"model", req.Model,
			"project_id", p.getProjectID(),
			"error", err,
			"error_type", fmt.Sprintf("%T", err))
		return nil, p.wrapError(err)
	}

	return gemini.FromGeminiResponse(resp, req.Model), nil
}

// getProjectID returns the project ID for this provider (for debugging)
func (p *AntigravityProvider) getProjectID() string {
	if p.geminiAuth != nil {
		return p.geminiAuth.GetProjectID()
	}
	return "unknown"
}

func (p *AntigravityProvider) StreamChatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (<-chan openai.ChatCompletionStreamResponse, <-chan error) {
	respChan := make(chan openai.ChatCompletionStreamResponse)
	errChan := make(chan error, 1)

	sys, contents, config, err := gemini.ToGeminiRequest(req)
	if err != nil {
		errChan <- p.wrapError(err)
		close(respChan)
		close(errChan)
		return respChan, errChan
	}

	if sys != nil {
		config.SystemInstruction = sys
	}

	go func() {
		defer close(respChan)
		defer close(errChan)

		iter := p.client.Models.GenerateContentStream(ctx, req.Model, contents, config)
		for resp, err := range iter {
			if err != nil {
				errChan <- p.wrapError(err)
				return
			}

			chunk := gemini.FromGeminiChunk(resp, req.Model)
			respChan <- *chunk
		}
	}()

	return respChan, errChan
}

// wrapError converts genai errors to ProviderError with proper status codes
func (p *AntigravityProvider) wrapError(err error) error {
	errStr := err.Error()

	// Parse genai error format: "Error 429, Message: ..., Status: RESOURCE_EXHAUSTED, Details: [...]"
	statusCode := http.StatusInternalServerError
	message := errStr
	var details interface{}

	// Extract status code
	if idx := strings.Index(errStr, "Error "); idx >= 0 {
		var code int
		if _, scanErr := fmt.Sscanf(errStr[idx:], "Error %d", &code); scanErr == nil {
			statusCode = code
		}
	}

	// Extract message
	if idx := strings.Index(errStr, "Message: "); idx >= 0 {
		endIdx := strings.Index(errStr[idx:], ", Status:")
		if endIdx > 0 {
			message = errStr[idx+9 : idx+endIdx]
		}
	}

	// Extract status and details for additional context
	if idx := strings.Index(errStr, "Status: "); idx >= 0 {
		detailsIdx := strings.Index(errStr[idx:], "Details:")
		if detailsIdx > 0 {
			details = map[string]string{
				"status":  errStr[idx+8 : idx+detailsIdx-2],
				"details": errStr[idx+detailsIdx:],
			}
		}
	}

	return provider.NewProviderError(statusCode, message, p.name, details)
}

func (p *AntigravityProvider) ListModels(ctx context.Context) ([]string, error) {
	fallbackModels := []string{
		"gemini-2.5-computer-use-preview-10-2025",
		"gemini-3-pro-image-preview",
		"gemini-3-pro-preview",
		"gemini-3-flash",
		"gemini-2.5-flash",
		"gemini-claude-sonnet-4-5",
		"gemini-claude-sonnet-4-5-thinking",
		"gemini-claude-opus-4-5-thinking",
	}

	var token string
	var err error

	if p.auth != nil {
		token, err = p.auth.GetToken(ctx)
	} else if p.geminiAuth != nil {
		token, err = p.geminiAuth.GetToken(ctx)
	} else {
		utils.L().Warnf("No authenticator available for Antigravity models")
		return fallbackModels, nil
	}

	if err != nil {
		utils.L().Warnf("Failed to get token for Antigravity models: %v", err)
		return fallbackModels, nil
	}

	url := fmt.Sprintf("%s/%s:fetchAvailableModels", AntigravityBaseURL, APIVersion)
	req, err := http.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return fallbackModels, nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", DefaultUserAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		utils.L().Warnf("Error calling fetchAvailableModels: %v", err)
		return fallbackModels, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		utils.L().Warnf("fetchAvailableModels failed (%d): %s", resp.StatusCode, string(body))
		return fallbackModels, nil
	}

	var rawResponse map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&rawResponse); err != nil {
		utils.L().Warnf("Failed to decode Antigravity models: %v", err)
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
		return models, nil
	}

	return fallbackModels, nil
}

func (p *AntigravityProvider) SupportsModel(model string) bool {
	supportedModels, err := p.ListModels(context.Background())
	if err != nil {
		return false
	}

	for _, supported := range supportedModels {
		if supported == model {
			return true
		}
	}
	return false
}

// geminiTokenProvider adapts auth.GeminiAuthenticator to cloudauth.TokenProvider (like POC)
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

	// Log token info (first 20 chars only for security)
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

// discoverProjectIDWithGeminiAuth helps find the project ID needed for API using auth.GeminiAuthenticator (like POC)
func discoverProjectIDWithGeminiAuth(ctx context.Context, authenticator *auth.GeminiAuthenticator, baseURL string) (string, error) {
	// Force refresh at the beginning (like POC)
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

// discoverProjectID helps find the project ID needed for API
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
