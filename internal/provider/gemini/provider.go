package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	cloudauth "cloud.google.com/go/auth"
	"github.com/sashabaranov/go-openai"
	"google.golang.org/genai"
)

const (
	CloudCodeBaseURL = "https://cloudcode-pa.googleapis.com"
)

type GeminiProvider struct {
	client *genai.Client
	name   string
	auth   *Authenticator
}

// NewProvider creates a new Gemini provider with auth
// Note: This provider is NOT available for OpenAI-compatible API requests.
// It's kept for future native protocol implementation.
func NewProvider(ctx context.Context, name string, auth *Authenticator) (*GeminiProvider, error) {
	// 1. Discover Project ID
	projectID, err := discoverProjectID(ctx, auth, CloudCodeBaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to discover project ID: %w", err)
	}

	// 2. Create GenAI Client
	tokenProvider := &TokenProvider{authenticator: auth}
	creds := cloudauth.NewCredentials(&cloudauth.CredentialsOptions{
		TokenProvider: tokenProvider,
	})

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:     genai.BackendGeminiCLI,
		Project:     projectID,
		Credentials: creds,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create genai client: %w", err)
	}

	return &GeminiProvider{
		client: client,
		name:   name,
		auth:   auth,
	}, nil
}

func (p *GeminiProvider) Type() string {
	return "gemini"
}

func (p *GeminiProvider) Name() string {
	return p.name
}

// GetClient returns the underlying genai.Client for native protocol access
func (p *GeminiProvider) GetClient() *genai.Client {
	return p.client
}

// GetAuth returns the authenticator for native protocol access
func (p *GeminiProvider) GetAuth() *Authenticator {
	return p.auth
}

// ListModels returns a list of models supported by the provider
func (p *GeminiProvider) ListModels(ctx context.Context) ([]string, error) {
	return []string{
		"gemini-2.5-flash",
		"gemini-2.5-flash-lite",
		"gemini-2.5-pro",
		"gemini-2.5-pro-preview-06-05",
		"gemini-2.5-flash-preview-09-2025",
		"gemini-3-pro-preview",
		"gemini-3-flash-preview",
	}, nil
}

// SupportsModel checks if the provider supports the given model
func (p *GeminiProvider) SupportsModel(model string) bool {
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

// ChatCompletion is not supported for OpenAI-compatible API
// This provider is kept for future native protocol implementation
func (p *GeminiProvider) ChatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (interface{}, error) {
	return nil, fmt.Errorf("provider 'gemini' does not support OpenAI-compatible API. Use native protocol access instead")
}

// StreamChatCompletion is not supported for OpenAI-compatible API
// This provider is kept for future native protocol implementation
func (p *GeminiProvider) StreamChatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (<-chan openai.ChatCompletionStreamResponse, <-chan error) {
	errChan := make(chan error, 1)
	errChan <- fmt.Errorf("provider 'gemini' does not support OpenAI-compatible API. Use native protocol access instead")
	close(errChan)
	return make(chan openai.ChatCompletionStreamResponse), errChan
}

// discoverProjectID helps find the project ID needed for Gemini API
func discoverProjectID(ctx context.Context, authenticator *Authenticator, baseURL string) (string, error) {
	// Retry loop for handling 401 Unauthenticated
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
			// 401 Unauthenticated - try to refresh token and retry
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