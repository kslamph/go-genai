package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	cloudauth "cloud.google.com/go/auth"
	"github.com/sunbankio/omniproxy/internal/provider"
	"github.com/sunbankio/omniproxy/pkg/utils"
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
func NewProvider(ctx context.Context, name string, auth *Authenticator) (*GeminiProvider, error) {
	// 1. Discover Project ID
	projectID, err := DiscoverProjectID(ctx, auth, CloudCodeBaseURL)
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

func (p *GeminiProvider) Type() provider.ProviderType {
	return provider.ProviderGemini
}

func (p *GeminiProvider) Name() string {
	return p.name
}

func (p *GeminiProvider) SupportedProtocols() []provider.Protocol {
	return []provider.Protocol{provider.ProtocolGemini}
}

func (p *GeminiProvider) GetClient() *genai.Client {
	return p.client
}

func (p *GeminiProvider) GetAuth() *Authenticator {
	return p.auth
}

// RefreshClient recreates the genai.Client with fresh credentials after token refresh
// This is necessary because genai.Client caches tokens internally and doesn't automatically pick up refreshed tokens
func (p *GeminiProvider) RefreshClient(ctx context.Context) (*genai.Client, error) {
	// Discover Project ID (it shouldn't change, but we need it for the new client)
	projectID, err := DiscoverProjectID(ctx, p.auth, CloudCodeBaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to discover project ID during client refresh: %w", err)
	}

	// Create new TokenProvider with the refreshed authenticator
	tokenProvider := &TokenProvider{authenticator: p.auth}
	creds := cloudauth.NewCredentials(&cloudauth.CredentialsOptions{
		TokenProvider: tokenProvider,
	})

	// Create new genai.Client with fresh credentials
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:     genai.BackendGeminiCLI,
		Project:     projectID,
		Credentials: creds,
	})
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

// DiscoverProjectID helps find the project ID needed for Gemini API
func DiscoverProjectID(ctx context.Context, authenticator *Authenticator, baseURL string) (string, error) {
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