package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	cloudauth "cloud.google.com/go/auth"
	"github.com/sunbankio/omniproxy/internal/auth"
	"github.com/sunbankio/omniproxy/internal/config"
	"github.com/sunbankio/omniproxy/internal/provider"
	"github.com/sunbankio/omniproxy/internal/provider/antigravity"
	"github.com/sunbankio/omniproxy/internal/provider/gemini"
	"github.com/sunbankio/omniproxy/internal/provider/iflow"
	"github.com/sunbankio/omniproxy/internal/provider/qwen"
	"github.com/sunbankio/omniproxy/pkg/utils"
	"google.golang.org/genai"
)

// CredentialInitializer defines the contract for initializing credentials
type CredentialInitializer interface {
	Initialize(ctx context.Context, cfg *config.Config, registry *Registry, authManager auth.AuthManager) error
}

// CredentialFactory orchestrates the initialization of all credentials
type CredentialFactory struct {
	initializers map[auth.ProviderType]CredentialInitializer
}

// NewCredentialFactory creates a new credential factory
func NewCredentialFactory() *CredentialFactory {
	return &CredentialFactory{
		initializers: map[auth.ProviderType]CredentialInitializer{
			auth.ProviderTypeGemini:      NewGeminiCredentialInitializer(),
			auth.ProviderTypeAntigravity: NewAntigravityCredentialInitializer(),
			auth.ProviderTypeQwen:        NewQwenCredentialInitializer(),
			auth.ProviderTypeIFlow:       NewIFlowCredentialInitializer(),
		},
	}
}

// InitializeAllCredentials initializes all configured credentials
func (f *CredentialFactory) InitializeAllCredentials(ctx context.Context, cfg *config.Config, registry *Registry, authManager auth.AuthManager) error {
	for providerType, initializer := range f.initializers {
		if err := initializer.Initialize(ctx, cfg, registry, authManager); err != nil {
			utils.L().Errorf("Failed to initialize credential type %s: %v", providerType, err)
			// Continue with other providers
		}
	}
	return nil
}

// BaseCredentialInitializer contains common logic
type BaseCredentialInitializer struct {
	providerType     provider.ProviderType // Using old provider type for config compatibility
	authProviderType auth.ProviderType
	defaultCredsPath string
}

func (b *BaseCredentialInitializer) getCredentialPaths(cfg *config.Config) []string {
	return append([]string{b.defaultCredsPath}, cfg.Credentials[string(b.providerType)]...)
}

// initializeCredential handles the common pattern of processing a credential path
func (b *BaseCredentialInitializer) initializeCredential(
	ctx context.Context,
	index int,
	registry *Registry,
	authProviderType auth.ProviderType,
	getToken func() (string, error),
	configureCredential func(*auth.Credential),
	createProvider func(ctx context.Context, credID string) (provider.BaseProvider, error),
	getDefaultModels func() []string,
) error {
	// Get token
	token, err := getToken()
	if err != nil {
		return fmt.Errorf("failed to get token: %w", err)
	}

	// Create credential
	credID := fmt.Sprintf("%s-%d", authProviderType.String(), index)
	cred := auth.NewCredential(credID, authProviderType)
	cred.AccessToken = token

	// Configure credential with provider-specific settings
	configureCredential(cred)

	// Create provider
	providerInstance, err := createProvider(ctx, credID)
	if err != nil {
		utils.L().Warnf("Failed to create provider for %s: %v", credID, err)
		// Fallback to default models
		models := getDefaultModels()
		registry.RegisterCredential(cred, models)
		utils.L().Infof("Loaded credential: %s with %d fallback models", credID, len(models))
		return nil
	}

	cred.SetProvider(providerInstance)

	// Get models from provider
	models, err := providerInstance.ListModels(ctx)
	if err != nil {
		utils.L().Warnf("Failed to get models for credential %s: %v", credID, err)
		// Fallback to default models
		models = getDefaultModels()
	}
	registry.RegisterCredential(cred, models)

	utils.L().Infof("Loaded credential: %s with %d models", credID, len(models))
	return nil
}

// createGeminiAuthenticator creates a Gemini authenticator
func (g *GeminiCredentialInitializer) createGeminiAuthenticator(path string, index int) *gemini.Authenticator {
	return gemini.NewAuthenticator(&gemini.OAuthConfig{
		ClientID:     gemini.DefaultOAuthConfig().ClientID,
		ClientSecret: gemini.DefaultOAuthConfig().ClientSecret,
		Scope:        gemini.DefaultOAuthConfig().Scope,
		RedirectPort: gemini.DefaultOAuthConfig().RedirectPort + index,
		CredsPath:    path,
	})
}

// configureGeminiCredential configures a Gemini credential with project ID and expiry
func (g *GeminiCredentialInitializer) configureGeminiCredential(cred *auth.Credential, helper *gemini.Authenticator, path string) {
	// Discover Project ID using the official Google library method (this is critical for API access)
	tokenProvider := gemini.NewTokenProvider(helper)
	creds := cloudauth.NewCredentials(&cloudauth.CredentialsOptions{
		TokenProvider: tokenProvider,
	})

	projectID, err := genai.DiscoverCloudCodeProject(context.Background(), creds, genai.BackendGeminiCLI)
	if err != nil {
		utils.L().Warnf("Failed to discover project ID for %s: %v, using fallback", path, err)
		projectID = "genai-genesis" // Fallback
	}
	cred.ProjectID = projectID

	// Try to get expiry from file
	if data, err := os.ReadFile(path); err == nil {
		var fileCreds struct {
			ExpiryDate int64 `json:"expiry_date"`
		}
		if json.Unmarshal(data, &fileCreds) == nil && fileCreds.ExpiryDate > 0 {
			cred.Expiry = time.Unix(fileCreds.ExpiryDate, 0)
		}
	}
	if cred.Expiry.IsZero() {
		cred.Expiry = time.Now().Add(1 * time.Hour)
	}
}

// createGeminiProvider creates a Gemini provider
func (g *GeminiCredentialInitializer) createGeminiProvider(ctx context.Context, credID string, helper *gemini.Authenticator) (provider.BaseProvider, error) {
	return gemini.NewProvider(ctx, credID, helper)
}

// getGeminiDefaultModels returns the default models for Gemini
func (g *GeminiCredentialInitializer) getGeminiDefaultModels() []string {
	return []string{"gemini-1.5-pro", "gemini-1.5-flash", "gemini-2.0-flash-exp", "gemini-3-flash-preview"}
}

// GeminiCredentialInitializer initializes Gemini credentials
type GeminiCredentialInitializer struct {
	*BaseCredentialInitializer
}

func NewGeminiCredentialInitializer() *GeminiCredentialInitializer {
	return &GeminiCredentialInitializer{
		BaseCredentialInitializer: &BaseCredentialInitializer{
			providerType:     provider.ProviderGemini,
			authProviderType: auth.ProviderTypeGemini,
			defaultCredsPath: gemini.DefaultOAuthConfig().CredsPath,
		},
	}
}

func (g *GeminiCredentialInitializer) Initialize(ctx context.Context, cfg *config.Config, registry *Registry, authManager auth.AuthManager) error {
	paths := g.getCredentialPaths(cfg)
	for i, path := range paths {
		if _, err := os.Stat(path); err != nil {
			continue // Skip non-existent paths
		}

		helper := g.createGeminiAuthenticator(path, i)

		if err := g.initializeCredential(
			ctx,

			i,
			registry,
			auth.ProviderTypeGemini,
			func() (string, error) { return helper.GetToken(ctx) },
			func(cred *auth.Credential) { g.configureGeminiCredential(cred, helper, path) },
			func(ctx context.Context, credID string) (provider.BaseProvider, error) {
				return g.createGeminiProvider(ctx, credID, helper)
			},
			g.getGeminiDefaultModels,
		); err != nil {
			utils.L().Warnf("Failed to process Gemini credential from %s: %v", path, err)
		}
	}
	return nil
}

// createAntigravityAuthenticator creates an Antigravity authenticator
func (a *AntigravityCredentialInitializer) createAntigravityAuthenticator(path string, index int) *antigravity.Authenticator {
	return antigravity.NewAuthenticator(&antigravity.OAuthConfig{
		ClientID:     antigravity.DefaultOAuthConfig().ClientID,
		ClientSecret: antigravity.DefaultOAuthConfig().ClientSecret,
		Scope:        antigravity.DefaultOAuthConfig().Scope,
		RedirectPort: antigravity.DefaultOAuthConfig().RedirectPort + index,
		CredsDir:     filepath.Dir(path),
		CredsFile:    filepath.Base(path),
	})
}

// configureAntigravityCredential configures an Antigravity credential with project ID and expiry
func (a *AntigravityCredentialInitializer) configureAntigravityCredential(cred *auth.Credential, helper *antigravity.Authenticator) {
	// Discover Project ID using the official Google library method (this is critical for API access)
	tokenProvider := antigravity.NewTokenProvider(helper)
	creds := cloudauth.NewCredentials(&cloudauth.CredentialsOptions{
		TokenProvider: tokenProvider,
	})

	projectID, err := genai.DiscoverCloudCodeProject(context.Background(), creds, genai.BackendAntigravity)
	if err != nil {
		utils.L().Warnf("Failed to discover project ID for Antigravity: %v, using fallback", err)
		projectID = "substantial-dragon-7kd70" // Fallback to your actual project
	}
	cred.ProjectID = projectID

	if expiry := helper.GetExpiryDate(); expiry > 0 {
		cred.Expiry = time.Unix(expiry, 0)
	} else {
		cred.Expiry = time.Now().Add(1 * time.Hour)
	}
}

// createAntigravityProvider creates an Antigravity provider
func (a *AntigravityCredentialInitializer) createAntigravityProvider(ctx context.Context, credID string, helper *antigravity.Authenticator) (provider.BaseProvider, error) {
	return antigravity.NewProviderWithGeminiAuth(ctx, credID, helper)
}

// getAntigravityDefaultModels returns the default models for Antigravity
func (a *AntigravityCredentialInitializer) getAntigravityDefaultModels() []string {
	return []string{"gemini-1.5-pro-002", "gemini-1.5-flash-002"}
}

// AntigravityCredentialInitializer initializes Antigravity credentials
type AntigravityCredentialInitializer struct {
	*BaseCredentialInitializer
}

func NewAntigravityCredentialInitializer() *AntigravityCredentialInitializer {
	homeDir, _ := os.UserHomeDir()
	defaultCredsPath := filepath.Join(homeDir, antigravity.DefaultOAuthConfig().CredsDir, antigravity.DefaultOAuthConfig().CredsFile)
	return &AntigravityCredentialInitializer{
		BaseCredentialInitializer: &BaseCredentialInitializer{
			providerType:     provider.ProviderAntigravity,
			authProviderType: auth.ProviderTypeAntigravity,
			defaultCredsPath: defaultCredsPath,
		},
	}
}

func (a *AntigravityCredentialInitializer) Initialize(ctx context.Context, cfg *config.Config, registry *Registry, authManager auth.AuthManager) error {
	paths := a.getCredentialPaths(cfg)
	for i, path := range paths {
		if _, err := os.Stat(path); err != nil {
			continue // Skip non-existent paths
		}

		helper := a.createAntigravityAuthenticator(path, i)

		if err := a.initializeCredential(
			ctx,
			i,
			registry,
			auth.ProviderTypeAntigravity,
			func() (string, error) { return helper.GetToken(ctx) },
			func(cred *auth.Credential) { a.configureAntigravityCredential(cred, helper) },
			func(ctx context.Context, credID string) (provider.BaseProvider, error) {
				return a.createAntigravityProvider(ctx, credID, helper)
			},
			a.getAntigravityDefaultModels,
		); err != nil {
			utils.L().Warnf("Failed to process Antigravity credential from %s: %v", path, err)
		}
	}
	return nil
}

// QwenCredentialInitializer
type QwenCredentialInitializer struct {
	*BaseCredentialInitializer
}

func NewQwenCredentialInitializer() *QwenCredentialInitializer {
	return &QwenCredentialInitializer{
		BaseCredentialInitializer: &BaseCredentialInitializer{
			providerType:     provider.ProviderQwen,
			authProviderType: auth.ProviderTypeQwen,
			defaultCredsPath: qwen.DefaultOAuthConfig().CredsPath,
		},
	}
}

func (q *QwenCredentialInitializer) Initialize(ctx context.Context, cfg *config.Config, registry *Registry, authManager auth.AuthManager) error {
	paths := q.getCredentialPaths(cfg)
	for i, path := range paths {
		if _, err := os.Stat(path); err == nil {
			helper := qwen.NewAuthenticator(&qwen.OAuthConfig{
				ClientID:      qwen.DefaultOAuthConfig().ClientID,
				Scope:         qwen.DefaultOAuthConfig().Scope,
				TokenURL:      qwen.DefaultOAuthConfig().TokenURL,
				DeviceAuthURL: qwen.DefaultOAuthConfig().DeviceAuthURL,
				CredsPath:     path,
			})

			token, err := helper.GetToken(ctx)
			if err != nil {
				continue
			}

			credID := fmt.Sprintf("qwen-%d", i)
			cred := auth.NewCredential(credID, auth.ProviderTypeQwen)
			cred.AccessToken = token
			cred.Expiry = time.Now().Add(24 * time.Hour) // Qwen tokens last longer usually

			// Create and set the provider instance
			qwenProvider := qwen.NewProvider(credID, helper)
			cred.SetProvider(qwenProvider)

			// Get models from the provider
			models, err := qwenProvider.ListModels(ctx)
			if err != nil {
				utils.L().Warnf("Failed to get models for Qwen credential %s: %v", credID, err)
				// Fallback to default models
				models = []string{"qwen3-coder-plus", "qwen3-coder-flash"}
			}
			registry.RegisterCredential(cred, models)

			utils.L().Infof("Loaded Qwen credential: %s with %d models", credID, len(models))
		}
	}
	return nil
}

// IFlowCredentialInitializer
type IFlowCredentialInitializer struct {
	*BaseCredentialInitializer
}

func NewIFlowCredentialInitializer() *IFlowCredentialInitializer {
	return &IFlowCredentialInitializer{
		BaseCredentialInitializer: &BaseCredentialInitializer{
			providerType:     provider.ProviderIFlow,
			authProviderType: auth.ProviderTypeIFlow,
			defaultCredsPath: iflow.DefaultOAuthConfig().CredsPath,
		},
	}
}

func (i *IFlowCredentialInitializer) Initialize(ctx context.Context, cfg *config.Config, registry *Registry, authManager auth.AuthManager) error {
	paths := i.getCredentialPaths(cfg)
	for idx, path := range paths {
		if _, err := os.Stat(path); err == nil {
			helper := iflow.NewAuthenticator(&iflow.OAuthConfig{
				ClientID:     iflow.DefaultOAuthConfig().ClientID,
				ClientSecret: iflow.DefaultOAuthConfig().ClientSecret,
				RedirectPort: iflow.DefaultOAuthConfig().RedirectPort + idx,
				CredsPath:    path,
			})

			token, err := helper.GetToken(ctx)
			if err != nil {
				continue
			}

			credID := fmt.Sprintf("iflow-%d", idx)
			cred := auth.NewCredential(credID, auth.ProviderTypeIFlow)
			cred.AccessToken = token
			cred.Expiry = time.Now().Add(1 * time.Hour)

			// Create and set the provider instance
			iflowProvider := iflow.NewProvider(credID, helper)
			cred.SetProvider(iflowProvider)

			// Get models from the provider
			models, err := iflowProvider.ListModels(ctx)
			if err != nil {
				utils.L().Warnf("Failed to get models for IFlow credential %s: %v", credID, err)
				// Fallback to default models
				models = []string{"qwen3-coder-plus", "qwen3-max", "qwen3-vl-plus", "kimi-k2-0905", "kimi-k2", "glm-4.6", "glm-4.7", "deepseek-v3.2", "deepseek-r1"}
			}
			registry.RegisterCredential(cred, models)

			utils.L().Infof("Loaded IFlow credential: %s with %d models", credID, len(models))
		}
	}
	return nil
}
