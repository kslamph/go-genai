package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sunbankio/omniproxy/internal/config"
	"github.com/sunbankio/omniproxy/internal/provider"
	"github.com/sunbankio/omniproxy/internal/provider/antigravity"
	"github.com/sunbankio/omniproxy/internal/provider/gemini"
	"github.com/sunbankio/omniproxy/internal/provider/iflow"
	"github.com/sunbankio/omniproxy/internal/provider/qwen"
	"github.com/sunbankio/omniproxy/pkg/utils"
)

// ProviderInitializer defines the contract for initializing a provider
type ProviderInitializer interface {
	Initialize(ctx context.Context, cfg *config.Config, registry *ProviderRegistry) error
}

// BaseProviderInitializer contains common logic for file-based credential discovery
type BaseProviderInitializer struct {
	providerType provider.ProviderType
	defaultCredsPath string
}

// NewBaseProviderInitializer creates a new base initializer
func NewBaseProviderInitializer(providerType provider.ProviderType, defaultCredsPath string) *BaseProviderInitializer {
	return &BaseProviderInitializer{
		providerType:    providerType,
		defaultCredsPath: defaultCredsPath,
	}
}

// getCredentialPaths returns a list of potential credential paths
func (b *BaseProviderInitializer) getCredentialPaths(cfg *config.Config) []string {
	return append([]string{b.defaultCredsPath}, cfg.Credentials[string(b.providerType)]...)
}

// GeminiInitializer initializes Gemini providers
type GeminiInitializer struct {
	*BaseProviderInitializer
}

// NewGeminiInitializer creates a new Gemini initializer
func NewGeminiInitializer() *GeminiInitializer {
	return &GeminiInitializer{
		BaseProviderInitializer: NewBaseProviderInitializer(provider.ProviderGemini, gemini.DefaultOAuthConfig().CredsPath),
	}
}

// Initialize initializes all Gemini providers
func (g *GeminiInitializer) Initialize(ctx context.Context, cfg *config.Config, registry *ProviderRegistry) error {
	paths := g.getCredentialPaths(cfg)
	for i, path := range paths {
		if _, err := os.Stat(path); err == nil {
			auth := gemini.NewAuthenticator(&gemini.OAuthConfig{
				ClientID:     gemini.DefaultOAuthConfig().ClientID,
				ClientSecret: gemini.DefaultOAuthConfig().ClientSecret,
				Scope:        gemini.DefaultOAuthConfig().Scope,
				RedirectPort: gemini.DefaultOAuthConfig().RedirectPort + i,
				CredsPath:    path,
			})
			p, err := gemini.NewProvider(ctx, fmt.Sprintf("gemini-%d", i), auth)
			if err == nil {
				registry.RegisterGeminiProvider(provider.ProviderGemini, p)
				utils.L().Infof("Loaded Gemini provider: %s from %s", p.Name(), path)
			} else {
				utils.L().Warnf("Failed to load Gemini provider from %s: %v", path, err)
			}
		}
	}
	return nil
}

// AntigravityInitializer initializes Antigravity providers
type AntigravityInitializer struct {
	*BaseProviderInitializer
}

// NewAntigravityInitializer creates a new Antigravity initializer
func NewAntigravityInitializer() *AntigravityInitializer {
	homeDir, _ := os.UserHomeDir()
	defaultCredsPath := filepath.Join(homeDir, antigravity.DefaultOAuthConfig().CredsDir, antigravity.DefaultOAuthConfig().CredsFile)
	return &AntigravityInitializer{
		BaseProviderInitializer: NewBaseProviderInitializer(provider.ProviderAntigravity, defaultCredsPath),
	}
}

// Initialize initializes all Antigravity providers
func (a *AntigravityInitializer) Initialize(ctx context.Context, cfg *config.Config, registry *ProviderRegistry) error {
	paths := a.getCredentialPaths(cfg)
	for i, path := range paths {
		if _, err := os.Stat(path); err == nil {
			homeDir, _ := os.UserHomeDir()
			defaultCredsPath := filepath.Join(homeDir, antigravity.DefaultOAuthConfig().CredsDir, antigravity.DefaultOAuthConfig().CredsFile)

			credsDir := antigravity.DefaultOAuthConfig().CredsDir
			credsFile := antigravity.DefaultOAuthConfig().CredsFile

			if path != defaultCredsPath {
				absPath := path
				if !filepath.IsAbs(path) {
					if wd, err := os.Getwd(); err == nil {
						absPath = filepath.Join(wd, path)
					}
				}
				credsDir = filepath.Dir(absPath)
				credsFile = filepath.Base(absPath)

				if homeDir, err := os.UserHomeDir(); err == nil {
					if strings.HasPrefix(absPath, homeDir+string(filepath.Separator)) {
						relPath := strings.TrimPrefix(absPath, homeDir+string(filepath.Separator))
						credsDir = filepath.Dir(relPath)
						credsFile = filepath.Base(relPath)
					}
				}
			}

			antigravityAuth := antigravity.NewAuthenticator(&antigravity.OAuthConfig{
				ClientID:     antigravity.DefaultOAuthConfig().ClientID,
				ClientSecret: antigravity.DefaultOAuthConfig().ClientSecret,
				Scope:        antigravity.DefaultOAuthConfig().Scope,
				RedirectPort: antigravity.DefaultOAuthConfig().RedirectPort + i,
				CredsDir:     credsDir,
				CredsFile:    credsFile,
			})

			p, err := antigravity.NewProviderWithGeminiAuth(ctx, fmt.Sprintf("antigravity-%d", i), antigravityAuth)
			if err == nil {
				registry.RegisterGeminiProvider(provider.ProviderAntigravity, p)
				utils.L().Infof("Loaded Antigravity provider: %s from %s", p.Name(), path)
			} else {
				utils.L().Warnf("Failed to load Antigravity provider from %s: %v", path, err)
			}
		}
	}
	return nil
}

// QwenInitializer initializes Qwen providers
type QwenInitializer struct {
	*BaseProviderInitializer
}

// NewQwenInitializer creates a new Qwen initializer
func NewQwenInitializer() *QwenInitializer {
	return &QwenInitializer{
		BaseProviderInitializer: NewBaseProviderInitializer(provider.ProviderQwen, qwen.DefaultOAuthConfig().CredsPath),
	}
}

// Initialize initializes all Qwen providers
func (q *QwenInitializer) Initialize(ctx context.Context, cfg *config.Config, registry *ProviderRegistry) error {
	paths := q.getCredentialPaths(cfg)
	for i, path := range paths {
		if _, err := os.Stat(path); err == nil {
			auth := qwen.NewAuthenticator(&qwen.OAuthConfig{
				ClientID:      qwen.DefaultOAuthConfig().ClientID,
				Scope:         qwen.DefaultOAuthConfig().Scope,
				TokenURL:      qwen.DefaultOAuthConfig().TokenURL,
				DeviceAuthURL: qwen.DefaultOAuthConfig().DeviceAuthURL,
				CredsPath:     path,
			})
			p := qwen.NewProvider(fmt.Sprintf("qwen-%d", i), auth)
			registry.RegisterOpenAIProvider(provider.ProviderQwen, p)
			utils.L().Infof("Loaded Qwen provider: %s from %s", p.Name(), path)
		}
	}
	return nil
}

// IFlowInitializer initializes IFlow providers
type IFlowInitializer struct {
	*BaseProviderInitializer
}

// NewIFlowInitializer creates a new IFlow initializer
func NewIFlowInitializer() *IFlowInitializer {
	return &IFlowInitializer{
		BaseProviderInitializer: NewBaseProviderInitializer(provider.ProviderIFlow, iflow.DefaultOAuthConfig().CredsPath),
	}
}

// Initialize initializes all IFlow providers
func (i *IFlowInitializer) Initialize(ctx context.Context, cfg *config.Config, registry *ProviderRegistry) error {
	paths := i.getCredentialPaths(cfg)
	for j, path := range paths {
		if _, err := os.Stat(path); err == nil {
			auth := iflow.NewAuthenticator(&iflow.OAuthConfig{
				ClientID:     iflow.DefaultOAuthConfig().ClientID,
				ClientSecret: iflow.DefaultOAuthConfig().ClientSecret,
				RedirectPort: iflow.DefaultOAuthConfig().RedirectPort + j,
				CredsPath:    path,
			})
			p := iflow.NewProvider(fmt.Sprintf("iflow-%d", j), auth)
			registry.RegisterOpenAIProvider(provider.ProviderIFlow, p)
			utils.L().Infof("Loaded IFlow provider: %s from %s", p.Name(), path)
		}
	}
	return nil
}

// ProviderFactory orchestrates the initialization of all providers
type ProviderFactory struct {
	initializers map[provider.ProviderType]ProviderInitializer
}

// NewProviderFactory creates a new provider factory
func NewProviderFactory() *ProviderFactory {
	return &ProviderFactory{
		initializers: map[provider.ProviderType]ProviderInitializer{
			provider.ProviderGemini:      NewGeminiInitializer(),
			provider.ProviderAntigravity: NewAntigravityInitializer(),
			provider.ProviderQwen:        NewQwenInitializer(),
			provider.ProviderIFlow:       NewIFlowInitializer(),
		},
	}
}

// InitializeAllProviders initializes all configured providers
func (f *ProviderFactory) InitializeAllProviders(ctx context.Context, cfg *config.Config, registry *ProviderRegistry) error {
	for providerType, initializer := range f.initializers {
		if err := initializer.Initialize(ctx, cfg, registry); err != nil {
			utils.L().Errorf("Failed to initialize provider type %s: %v", providerType, err)
			// Continue with other providers
		}
	}
	return nil
}