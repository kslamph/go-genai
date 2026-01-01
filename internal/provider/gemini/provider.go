package gemini

import (
	"context"
	"fmt"

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
	// 1. Discover Project ID using the official Google library method
	tokenProvider := &TokenProvider{authenticator: auth}
	creds := cloudauth.NewCredentials(&cloudauth.CredentialsOptions{
		TokenProvider: tokenProvider,
	})

	projectID, err := genai.DiscoverCloudCodeProject(ctx, creds, genai.BackendGeminiCLI)
	if err != nil {
		return nil, fmt.Errorf("failed to discover project ID: %w", err)
	}

	// 2. Create GenAI Client
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
	// Create new TokenProvider with the refreshed authenticator
	tokenProvider := &TokenProvider{authenticator: p.auth}
	creds := cloudauth.NewCredentials(&cloudauth.CredentialsOptions{
		TokenProvider: tokenProvider,
	})

	// Discover Project ID using the official Google library method
	projectID, err := genai.DiscoverCloudCodeProject(ctx, creds, genai.BackendGeminiCLI)
	if err != nil {
		return nil, fmt.Errorf("failed to discover project ID during client refresh: %w", err)
	}

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
		// "gemini-2.5-pro-preview-06-05",
		// "gemini-2.5-flash-preview-09-2025",
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
