package manager

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"sync"
	"time"

	"github.com/sunbankio/omniproxy/internal/config"
	"github.com/sunbankio/omniproxy/internal/provider"
	"github.com/sunbankio/omniproxy/internal/provider/antigravity"
	"github.com/sunbankio/omniproxy/internal/provider/gemini"
	"github.com/sunbankio/omniproxy/internal/provider/iflow"
	"github.com/sunbankio/omniproxy/internal/provider/kiro"
	"github.com/sunbankio/omniproxy/internal/provider/qwen"
	"github.com/sunbankio/omniproxy/pkg/utils"
)

// PoolManager manages pools of provider instances
type PoolManager struct {
	// pools maps provider type to a list of instances
	pools map[string][]provider.Provider

	// lastSuccess maps model name to the last successful provider instance
	lastSuccess map[string]provider.Provider

	// mu protects lastSuccess
	mu sync.RWMutex

	// rng for random selection
	rng *rand.Rand
}

// NewPoolManager creates a new PoolManager and initializes providers
func NewPoolManager(ctx context.Context, cfg *config.Config) (*PoolManager, error) {
	pm := &PoolManager{
		pools:       make(map[string][]provider.Provider),
		lastSuccess: make(map[string]provider.Provider),
		rng:         rand.New(rand.NewSource(time.Now().UnixNano())),
	}

	if err := pm.initProviders(ctx, cfg); err != nil {
		return nil, err
	}

	return pm, nil
}

func (pm *PoolManager) initProviders(ctx context.Context, cfg *config.Config) error {
	// 1. Initialize Gemini
	geminiPaths := append([]string{gemini.DefaultOAuthConfig().CredsPath}, cfg.Credentials["gemini"]...)
	for i, path := range geminiPaths {
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
				pm.pools["gemini"] = append(pm.pools["gemini"], p)
				utils.L().Infof("Loaded Gemini provider: %s from %s", p.Name(), path)
			} else {
				utils.L().Warnf("Failed to load Gemini provider from %s: %v", path, err)
			}
		}
	}

	// 2. Initialize Antigravity
	antigravityPaths := append([]string{antigravity.DefaultOAuthConfig().CredsPath}, cfg.Credentials["antigravity"]...)
	for i, path := range antigravityPaths {
		if _, err := os.Stat(path); err == nil {
			auth := antigravity.NewAuthenticator(&antigravity.OAuthConfig{
				ClientID:     antigravity.DefaultOAuthConfig().ClientID,
				ClientSecret: antigravity.DefaultOAuthConfig().ClientSecret,
				Scope:        antigravity.DefaultOAuthConfig().Scope,
				RedirectPort: antigravity.DefaultOAuthConfig().RedirectPort + i,
				CredsPath:    path,
			})
			p, err := antigravity.NewProvider(ctx, fmt.Sprintf("antigravity-%d", i), auth)
			if err == nil {
				pm.pools["antigravity"] = append(pm.pools["antigravity"], p)
				utils.L().Infof("Loaded Antigravity provider: %s from %s", p.Name(), path)
			} else {
				utils.L().Warnf("Failed to load Antigravity provider from %s: %v", path, err)
			}
		}
	}

	// 3. Initialize Kiro
	kiroPaths := append([]string{kiro.DefaultOAuthConfig().CredsPath}, cfg.Credentials["kiro"]...)
	for i, path := range kiroPaths {
		if _, err := os.Stat(path); err == nil {
			auth := kiro.NewAuthenticator(&kiro.OAuthConfig{
				Region:        kiro.DefaultOAuthConfig().Region,
				RefreshURL:    kiro.DefaultOAuthConfig().RefreshURL,
				RefreshIDCURL: kiro.DefaultOAuthConfig().RefreshIDCURL,
				BaseURL:       kiro.DefaultOAuthConfig().BaseURL,
				CredsPath:     path,
			})
			p := kiro.NewProvider(fmt.Sprintf("kiro-%d", i), auth)
			pm.pools["kiro"] = append(pm.pools["kiro"], p)
			utils.L().Infof("Loaded Kiro provider: %s from %s", p.Name(), path)
		}
	}

	// 4. Initialize Qwen
	qwenPaths := append([]string{qwen.DefaultOAuthConfig().CredsPath}, cfg.Credentials["qwen"]...)
	for i, path := range qwenPaths {
		if _, err := os.Stat(path); err == nil {
			auth := qwen.NewAuthenticator(&qwen.OAuthConfig{
				ClientID:      qwen.DefaultOAuthConfig().ClientID,
				Scope:         qwen.DefaultOAuthConfig().Scope,
				TokenURL:      qwen.DefaultOAuthConfig().TokenURL,
				DeviceAuthURL: qwen.DefaultOAuthConfig().DeviceAuthURL,
				CredsPath:     path,
			})
			p := qwen.NewProvider(fmt.Sprintf("qwen-%d", i), auth)
			pm.pools["qwen"] = append(pm.pools["qwen"], p)
			utils.L().Infof("Loaded Qwen provider: %s from %s", p.Name(), path)
		}
	}

	// 5. Initialize IFlow
	iflowPaths := append([]string{iflow.DefaultOAuthConfig().CredsPath}, cfg.Credentials["iflow"]...)
	for i, path := range iflowPaths {
		if _, err := os.Stat(path); err == nil {
			auth := iflow.NewAuthenticator(&iflow.OAuthConfig{
				ClientID:     iflow.DefaultOAuthConfig().ClientID,
				ClientSecret: iflow.DefaultOAuthConfig().ClientSecret,
				RedirectPort: iflow.DefaultOAuthConfig().RedirectPort + i,
				CredsPath:    path,
			})
			p := iflow.NewProvider(fmt.Sprintf("iflow-%d", i), auth)
			pm.pools["iflow"] = append(pm.pools["iflow"], p)
			utils.L().Infof("Loaded IFlow provider: %s from %s", p.Name(), path)
		}
	}

	// 6. Initialize OpenAI (if configured via some means, for now just placeholder)
	// We could add standard OpenAI providers here too if they were in the config.

	return nil
}

// GetProvider returns a provider for the given type and model
func (pm *PoolManager) GetProvider(providerType string, model string) (provider.Provider, error) {
	pm.mu.RLock()
	last := pm.lastSuccess[model]
	pm.mu.RUnlock()

	// If last successful provider for this model matches the type, use it
	if last != nil && last.Type() == providerType {
		return last, nil
	}

	// Otherwise, select a random one from the pool
	pool := pm.pools[providerType]
	if len(pool) == 0 {
		return nil, fmt.Errorf("no providers available for type: %s", providerType)
	}

	return pool[pm.rng.Intn(len(pool))], nil
}

// GetProviderByModel finds a provider by model name across all pools
func (pm *PoolManager) GetProviderByModel(model string) (provider.Provider, error) {
	pm.mu.RLock()
	last := pm.lastSuccess[model]
	pm.mu.RUnlock()

	if last != nil {
		return last, nil
	}

	// Find all providers that support this model
	var candidates []provider.Provider
	for _, pool := range pm.pools {
		for _, p := range pool {
			// We need a way to check if a provider supports a model
			// For now, we assume if they exist in the pool, we'll try them
			// In a real scenario, we'd have a ModelSupported(model) method.
			candidates = append(candidates, p)
		}
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no providers found for model: %s", model)
	}

	return candidates[pm.rng.Intn(len(candidates))], nil
}

// RecordSuccess records a successful request for a model
func (pm *PoolManager) RecordSuccess(model string, p provider.Provider) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.lastSuccess[model] = p
}

// ListModels returns a list of all models from all providers
func (pm *PoolManager) ListModels(ctx context.Context) ([]string, error) {
	uniqueModels := make(map[string]bool)
	var models []string

	// Iterate over all providers
	// To avoid too many calls, maybe we just call one from each pool?
	// But different credentials might (unlikely) see different models.
	// Typically models are per provider type.
	
	for _, pool := range pm.pools {
		if len(pool) > 0 {
			// Just ask the first provider in the pool
			p := pool[0]
			ms, err := p.ListModels(ctx)
			if err != nil {
				utils.L().Warnf("Failed to list models from provider %s: %v", p.Name(), err)
				continue
			}
			for _, m := range ms {
				if !uniqueModels[m] {
					uniqueModels[m] = true
					models = append(models, m)
				}
			}
		}
	}
	return models, nil
}

// ListProviderModels returns models for a specific provider type
func (pm *PoolManager) ListProviderModels(ctx context.Context, providerType string) ([]string, error) {
	pool, ok := pm.pools[providerType]
	if !ok || len(pool) == 0 {
		return nil, fmt.Errorf("no providers available for type: %s", providerType)
	}

	// Just ask the first one
	return pool[0].ListModels(ctx)
}
