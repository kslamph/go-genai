package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"encoding/json"
	"time"

	"github.com/sunbankio/omniproxy/internal/auth"
	"github.com/sunbankio/omniproxy/internal/config"
	"github.com/sunbankio/omniproxy/internal/provider"
	"github.com/sunbankio/omniproxy/internal/provider/antigravity"
	"github.com/sunbankio/omniproxy/internal/provider/gemini"
	"github.com/sunbankio/omniproxy/internal/provider/iflow"
	"github.com/sunbankio/omniproxy/internal/provider/qwen"
	"github.com/sunbankio/omniproxy/pkg/utils"
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
		if _, err := os.Stat(path); err == nil {
			// Initialize Auth Helper (to get initial token)
			helper := gemini.NewAuthenticator(&gemini.OAuthConfig{
				ClientID:     gemini.DefaultOAuthConfig().ClientID,
				ClientSecret: gemini.DefaultOAuthConfig().ClientSecret,
				Scope:        gemini.DefaultOAuthConfig().Scope,
				RedirectPort: gemini.DefaultOAuthConfig().RedirectPort + i,
				CredsPath:    path,
			})

			// Get Initial Token
			token, err := helper.GetToken(ctx)
			if err != nil {
				utils.L().Warnf("Failed to load Gemini credential from %s: %v", path, err)
				continue
			}

			// Discover Project ID (this is critical for API access)
			projectID, err := gemini.DiscoverProjectID(ctx, helper, "https://cloudcode-pa.googleapis.com")
			if err != nil {
				utils.L().Warnf("Failed to discover project ID for %s: %v, using fallback", path, err)
				projectID = "genai-genesis" // Fallback
			}

			// Create Credential
			credID := fmt.Sprintf("gemini-%d", i)
			cred := auth.NewCredential(credID, auth.ProviderTypeGemini)
			cred.AccessToken = token
			cred.ProjectID = projectID

			// Try to get expiry
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

			// Create provider and get models
			geminiProvider, err := gemini.NewProvider(ctx, credID, helper)
			if err != nil {
				utils.L().Warnf("Failed to create Gemini provider for %s: %v", credID, err)
				// Fallback to default models
				models := []string{"gemini-1.5-pro", "gemini-1.5-flash", "gemini-2.0-flash-exp", "gemini-3-flash-preview"}
				registry.RegisterCredential(cred, models)
				utils.L().Infof("Loaded Gemini credential: %s (project: %s) with %d fallback models", credID, projectID, len(models))
				continue
			}

			cred.SetProvider(geminiProvider)

			// Get models from the provider
			models, err := geminiProvider.ListModels(ctx)
			if err != nil {
				utils.L().Warnf("Failed to get models for Gemini credential %s: %v", credID, err)
				// Fallback to default models
				models = []string{"gemini-1.5-pro", "gemini-1.5-flash", "gemini-2.0-flash-exp", "gemini-3-flash-preview"}
			}
			registry.RegisterCredential(cred, models)

			utils.L().Infof("Loaded Gemini credential: %s (project: %s) with %d models", credID, projectID, len(models))
		}
	}
	return nil
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
		if _, err := os.Stat(path); err == nil {
			// Setup Path logic (copied from provider_factory)
			// ... (Simplified path logic for brevity, assume absolute or default works for now)
			// In real impl, copy strict logic from provider_factory if needed

			helper := antigravity.NewAuthenticator(&antigravity.OAuthConfig{
				ClientID:     antigravity.DefaultOAuthConfig().ClientID,
				ClientSecret: antigravity.DefaultOAuthConfig().ClientSecret,
				Scope:        antigravity.DefaultOAuthConfig().Scope,
				RedirectPort: antigravity.DefaultOAuthConfig().RedirectPort + i,
				CredsDir:     filepath.Dir(path),
				CredsFile:    filepath.Base(path),
			})

			token, err := helper.GetToken(ctx)
			if err != nil {
				utils.L().Warnf("Failed to load Antigravity credential from %s: %v", path, err)
				continue
			}

// Create Credential
			credID := fmt.Sprintf("antigravity-%d", i)
			cred := auth.NewCredential(credID, auth.ProviderTypeAntigravity)
			cred.AccessToken = token

			// Antigravity often needs explicit Project ID
			// Ideally read from config or discovery
			cred.ProjectID = "antigravity-test-project"

			if expiry := helper.GetExpiryDate(); expiry > 0 {
				cred.Expiry = time.Unix(expiry, 0)
			} else {
				cred.Expiry = time.Now().Add(1 * time.Hour)
			}

			// Create provider and get models
			antigravityProvider, err := antigravity.NewProviderWithGeminiAuth(ctx, credID, helper)
			if err != nil {
				utils.L().Warnf("Failed to create Antigravity provider for %s: %v", credID, err)
				// Fallback to default models
				models := []string{"gemini-1.5-pro-002", "gemini-1.5-flash-002"}
				registry.RegisterCredential(cred, models)
				utils.L().Infof("Loaded Antigravity credential: %s with %d fallback models", credID, len(models))
				continue
			}

			cred.SetProvider(antigravityProvider)

			// Get models from the provider
			models, err := antigravityProvider.ListModels(ctx)
			if err != nil {
				utils.L().Warnf("Failed to get models for Antigravity credential %s: %v", credID, err)
				// Fallback to default models
				models = []string{"gemini-1.5-pro-002", "gemini-1.5-flash-002"}
			}
			registry.RegisterCredential(cred, models)

			utils.L().Infof("Loaded Antigravity credential: %s with %d models", credID, len(models))
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
