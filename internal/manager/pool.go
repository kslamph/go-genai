package manager

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sunbankio/omniproxy/auth"
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

	// failureCount tracks recent failures per provider (for smart selection)
	failureCount map[string]int

	// lastUsedIndex tracks the last used index for round-robin per provider type
	lastUsedIndex map[string]int

	// mu protects lastSuccess, failureCount, and lastUsedIndex
	mu sync.RWMutex

	// rng for random selection (fallback only)
	rng *rand.Rand
}

// NewPoolManager creates a new PoolManager and initializes providers
func NewPoolManager(ctx context.Context, cfg *config.Config) (*PoolManager, error) {
	pm := &PoolManager{
		pools:         make(map[string][]provider.Provider),
		lastSuccess:   make(map[string]provider.Provider),
		failureCount:  make(map[string]int),
		lastUsedIndex: make(map[string]int),
		rng:           rand.New(rand.NewSource(time.Now().UnixNano())),
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
			// Parse path into CredsDir and CredsFile for auth.GeminiAuthenticator (like POC)
			credsDir := ".antigravity"
			credsFile := "oauth_creds.json"
			
			// If it's the default path, use standard values
			if path != antigravity.DefaultOAuthConfig().CredsPath {
				// For additional paths, extract directory and filename
				absPath := path
				if !filepath.IsAbs(path) {
					if wd, err := os.Getwd(); err == nil {
						absPath = filepath.Join(wd, path)
					}
				}
				credsDir = filepath.Dir(absPath)
				credsFile = filepath.Base(absPath)
				
				// Make relative to home if possible (like the POC)
				if homeDir, err := os.UserHomeDir(); err == nil {
					if strings.HasPrefix(absPath, homeDir+string(filepath.Separator)) {
						relPath := strings.TrimPrefix(absPath, homeDir+string(filepath.Separator))
						credsDir = filepath.Dir(relPath)
						credsFile = filepath.Base(relPath)
					}
				}
			}
			
			// Use auth.GeminiAuthenticator like the POC
			geminiAuth := auth.NewGeminiAuthenticator(&auth.GeminiOAuthConfig{
				ClientID:     antigravity.DefaultOAuthConfig().ClientID,
				ClientSecret: antigravity.DefaultOAuthConfig().ClientSecret,
				Scope:        antigravity.DefaultOAuthConfig().Scope,
				RedirectPort: antigravity.DefaultOAuthConfig().RedirectPort + i,
				CredsDir:     credsDir,
				CredsFile:    credsFile,
			})

			utils.L().Infof("Creating Antigravity provider %d with credsDir=%s, credsFile=%s, fullPath=%s",
				i, credsDir, credsFile, geminiAuth.GetCredentialsPath())

			// Create antigravity provider with gemini auth
			p, err := antigravity.NewProviderWithGeminiAuth(ctx, fmt.Sprintf("antigravity-%d", i), geminiAuth)
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
	p, _, err := pm.GetProviderWithReason(providerType, model)
	return p, err
}

// GetProviderWithReason returns a provider for the given type and model with selection reason
func (pm *PoolManager) GetProviderWithReason(providerType string, model string) (provider.Provider, string, error) {
	pm.mu.RLock()
	last := pm.lastSuccess[model]
	pm.mu.RUnlock()

	// If last successful provider for this model matches the type, use it
	if last != nil && last.Type() == providerType {
		return last, "last success", nil
	}

	// Otherwise, select using smart round-robin with failure tracking
	pool := pm.pools[providerType]
	if len(pool) == 0 {
		return nil, "", fmt.Errorf("no providers available for type: %s", providerType)
	}

	selected := pm.selectProviderWithFailureTracking(pool, providerType)
	return selected, "round-robin", nil
}

// selectProviderWithFailureTracking selects a provider using round-robin while preferring providers with fewer failures
func (pm *PoolManager) selectProviderWithFailureTracking(pool []provider.Provider, poolKey string) provider.Provider {
	if len(pool) == 1 {
		return pool[0]
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Find the provider with the minimum failure count
	minFailures := -1
	var candidates []int // indices of providers with minimum failures

	for i, p := range pool {
		failures := pm.failureCount[p.Name()]
		if minFailures == -1 || failures < minFailures {
			minFailures = failures
			candidates = []int{i}
		} else if failures == minFailures {
			candidates = append(candidates, i)
		}
	}

	// If multiple candidates with same failure count, use round-robin among them
	var selectedIdx int
	if len(candidates) == 1 {
		selectedIdx = candidates[0]
	} else {
		// Round-robin through candidates
		lastIdx := pm.lastUsedIndex[poolKey]

		// Find the next candidate after lastIdx
		found := false
		for _, idx := range candidates {
			if idx > lastIdx {
				selectedIdx = idx
				found = true
				break
			}
		}

		// If not found, wrap around to the first candidate
		if !found {
			selectedIdx = candidates[0]
		}
	}

	pm.lastUsedIndex[poolKey] = selectedIdx
	return pool[selectedIdx]
}

// GetProviderByModel finds a provider by model name across all pools
func (pm *PoolManager) GetProviderByModel(model string) (provider.Provider, error) {
	p, _, err := pm.GetProviderByModelWithReason(model)
	return p, err
}

// GetProviderByModelWithReason finds a provider by model name across all pools and returns the selection reason
// Note: This method searches across ALL providers. For OpenAI-compatible API, use GetProviderByModelForOpenAI.
func (pm *PoolManager) GetProviderByModelWithReason(model string) (provider.Provider, string, error) {
	pm.mu.RLock()
	last := pm.lastSuccess[model]
	pm.mu.RUnlock()

	// Check if last successful provider still supports this model
	if last != nil && last.SupportsModel(model) {
		return last, "last success", nil
	}

	// Find all providers that support this model
	var candidates []provider.Provider
	for _, pool := range pm.pools {
		for _, p := range pool {
			if p.SupportsModel(model) {
				candidates = append(candidates, p)
			}
		}
	}

	if len(candidates) == 0 {
		return nil, "", fmt.Errorf("no providers found for model: %s", model)
	}

	// Use smart selection with failure tracking
	selected := pm.selectProviderWithFailureTracking(candidates, "model:"+model)
	return selected, "round-robin", nil
}

// GetProviderByModelForOpenAI finds a provider by model name but only within OpenAI-compatible providers (qwen, iflow)
// This is used for the OpenAI-compatible API endpoints
func (pm *PoolManager) GetProviderByModelForOpenAI(model string) (provider.Provider, string, error) {
	pm.mu.RLock()
	last := pm.lastSuccess[model]
	pm.mu.RUnlock()

	// Check if last successful provider still supports this model and is OpenAI-compatible
	openAICompatibleTypes := map[string]bool{"qwen": true, "iflow": true}
	if last != nil && last.SupportsModel(model) && openAICompatibleTypes[last.Type()] {
		return last, "last success", nil
	}

	// Find all OpenAI-compatible providers that support this model
	var candidates []provider.Provider
	for _, poolType := range []string{"qwen", "iflow"} {
		if pool, ok := pm.pools[poolType]; ok {
			for _, p := range pool {
				if p.SupportsModel(model) {
					candidates = append(candidates, p)
				}
			}
		}
	}

	if len(candidates) == 0 {
		return nil, "", fmt.Errorf("no OpenAI-compatible providers found for model: %s", model)
	}

	// Use smart selection with failure tracking
	selected := pm.selectProviderWithFailureTracking(candidates, "openai-model:"+model)
	return selected, "round-robin", nil
}

// RecordSuccess records a successful request for a model
func (pm *PoolManager) RecordSuccess(model string, p provider.Provider) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.lastSuccess[model] = p
	// Reset failure count on success
	pm.failureCount[p.Name()] = 0
}

// RecordFailure records a failed request for a provider
func (pm *PoolManager) RecordFailure(p provider.Provider) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.failureCount[p.Name()]++
}

// ListModels returns a list of all models from OpenAI-compatible providers (qwen, iflow)
func (pm *PoolManager) ListModels(ctx context.Context) ([]string, error) {
	uniqueModels := make(map[string]bool)
	var models []string

	// Only iterate over OpenAI-compatible providers (qwen, iflow)
	for _, poolType := range []string{"qwen", "iflow"} {
		if pool, ok := pm.pools[poolType]; ok && len(pool) > 0 {
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
