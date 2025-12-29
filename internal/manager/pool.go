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

// PoolManager manages pools of provider instances separated by protocol type
type PoolManager struct {
	// OpenAI-compatible providers (qwen, iflow)
	openaiPools map[string][]provider.OpenAICompatibleProvider

	// Gemini-native providers (gemini, antigravity)
	geminiPools map[string][]provider.GeminiNativeProvider

	// Kiro-native providers
	kiroPools map[string][]provider.KiroNativeProvider

	// lastSuccess maps model name to the last successful provider instance
	lastSuccess map[string]provider.BaseProvider

	// failureCount tracks recent failures per provider (for smart selection)
	failureCount map[string]int

	// lastUsedIndex tracks the last used index for round-robin per provider type
	lastUsedIndex map[string]int

	// rateLimitRejectTime maps "providerName:model" to the timestamp when it was last rejected due to 429
	rateLimitRejectTime map[string]time.Time

	// quotaExhaustedResetTime maps "providerName:model" to the timestamp when quota will be reset
	quotaExhaustedResetTime map[string]time.Time

	// mu protects lastSuccess, failureCount, lastUsedIndex, rateLimitRejectTime, and quotaExhaustedResetTime
	mu sync.RWMutex

	// rng for random selection (fallback only)
	rng *rand.Rand
}

// NewPoolManager creates a new PoolManager and initializes providers
func NewPoolManager(ctx context.Context, cfg *config.Config) (*PoolManager, error) {
	pm := &PoolManager{
		openaiPools:            make(map[string][]provider.OpenAICompatibleProvider),
		geminiPools:            make(map[string][]provider.GeminiNativeProvider),
		kiroPools:              make(map[string][]provider.KiroNativeProvider),
		lastSuccess:            make(map[string]provider.BaseProvider),
		failureCount:           make(map[string]int),
		lastUsedIndex:          make(map[string]int),
		rateLimitRejectTime:    make(map[string]time.Time),
		quotaExhaustedResetTime: make(map[string]time.Time),
		rng:                    rand.New(rand.NewSource(time.Now().UnixNano())),
	}

	if err := pm.initProviders(ctx, cfg); err != nil {
		return nil, err
	}

	return pm, nil
}

func (pm *PoolManager) initProviders(ctx context.Context, cfg *config.Config) error {
	// 1. Initialize Gemini (Gemini-native)
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
				pm.geminiPools["gemini"] = append(pm.geminiPools["gemini"], p)
				utils.L().Infof("Loaded Gemini provider: %s from %s", p.Name(), path)
			} else {
				utils.L().Warnf("Failed to load Gemini provider from %s: %v", path, err)
			}
		}
	}

	// 2. Initialize Antigravity (Gemini-native)
	antigravityPaths := append([]string{antigravity.DefaultOAuthConfig().CredsPath}, cfg.Credentials["antigravity"]...)
	for i, path := range antigravityPaths {
		if _, err := os.Stat(path); err == nil {
			credsDir := ".antigravity"
			credsFile := "oauth_creds.json"

			if path != antigravity.DefaultOAuthConfig().CredsPath {
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

			geminiAuth := auth.NewGeminiAuthenticator(&auth.GeminiOAuthConfig{
				ClientID:     antigravity.DefaultOAuthConfig().ClientID,
				ClientSecret: antigravity.DefaultOAuthConfig().ClientSecret,
				Scope:        antigravity.DefaultOAuthConfig().Scope,
				RedirectPort: antigravity.DefaultOAuthConfig().RedirectPort + i,
				CredsDir:     credsDir,
				CredsFile:    credsFile,
			})

			p, err := antigravity.NewProviderWithGeminiAuth(ctx, fmt.Sprintf("antigravity-%d", i), geminiAuth)
			if err == nil {
				pm.geminiPools["antigravity"] = append(pm.geminiPools["antigravity"], p)
				utils.L().Infof("Loaded Antigravity provider: %s from %s", p.Name(), path)
			} else {
				utils.L().Warnf("Failed to load Antigravity provider from %s: %v", path, err)
			}
		}
	}

	// 3. Initialize Kiro (Kiro-native)
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
			pm.kiroPools["kiro"] = append(pm.kiroPools["kiro"], p)
			utils.L().Infof("Loaded Kiro provider: %s from %s", p.Name(), path)
		}
	}

	// 4. Initialize Qwen (OpenAI-compatible)
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
			pm.openaiPools["qwen"] = append(pm.openaiPools["qwen"], p)
			utils.L().Infof("Loaded Qwen provider: %s from %s", p.Name(), path)
		}
	}

	// 5. Initialize IFlow (OpenAI-compatible)
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
			pm.openaiPools["iflow"] = append(pm.openaiPools["iflow"], p)
			utils.L().Infof("Loaded IFlow provider: %s from %s", p.Name(), path)
		}
	}

	return nil
}

// GetOpenAIProvider returns an OpenAI-compatible provider for the given type and model
func (pm *PoolManager) GetOpenAIProvider(providerType string, model string) (provider.OpenAICompatibleProvider, error) {
	p, _, err := pm.GetOpenAIProviderWithReason(providerType, model)
	return p, err
}

// GetOpenAIProviderWithReason returns an OpenAI-compatible provider with selection reason
func (pm *PoolManager) GetOpenAIProviderWithReason(providerType string, model string) (provider.OpenAICompatibleProvider, string, error) {
	pm.mu.RLock()
	last := pm.lastSuccess[model]
	pm.mu.RUnlock()

	// If last successful provider for this model matches the type and is OpenAI-compatible, use it
	if last != nil {
		if openaiProvider, ok := last.(provider.OpenAICompatibleProvider); ok && openaiProvider.Type() == providerType {
			return openaiProvider, "last success", nil
		}
	}

	// Otherwise, select using smart round-robin with failure tracking
	pool := pm.openaiPools[providerType]
	if len(pool) == 0 {
		return nil, "", fmt.Errorf("no OpenAI-compatible providers available for type: %s", providerType)
	}

	selected := pm.selectOpenAIProviderWithFailureTracking(pool, providerType)
	return selected, "round-robin", nil
}

// selectOpenAIProviderWithFailureTracking selects a provider using round-robin while preferring providers with fewer failures
func (pm *PoolManager) selectOpenAIProviderWithFailureTracking(pool []provider.OpenAICompatibleProvider, poolKey string) provider.OpenAICompatibleProvider {
	if len(pool) == 1 {
		return pool[0]
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Find the provider with the minimum failure count
	minFailures := -1
	var candidates []int

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
		lastIdx := pm.lastUsedIndex[poolKey]

		found := false
		for _, idx := range candidates {
			if idx > lastIdx {
				selectedIdx = idx
				found = true
				break
			}
		}

		if !found {
			selectedIdx = candidates[0]
		}
	}

	pm.lastUsedIndex[poolKey] = selectedIdx
	return pool[selectedIdx]
}

// GetOpenAIProviderByModel finds an OpenAI-compatible provider by model name
func (pm *PoolManager) GetOpenAIProviderByModel(model string) (provider.OpenAICompatibleProvider, error) {
	p, _, err := pm.GetOpenAIProviderByModelWithReason(model)
	return p, err
}

// GetOpenAIProviderByModelWithReason finds an OpenAI-compatible provider by model name with reason
func (pm *PoolManager) GetOpenAIProviderByModelWithReason(model string) (provider.OpenAICompatibleProvider, string, error) {
	pm.mu.RLock()
	last := pm.lastSuccess[model]
	pm.mu.RUnlock()

	// Check if last successful provider still supports this model and is OpenAI-compatible
	if last != nil {
		if openaiProvider, ok := last.(provider.OpenAICompatibleProvider); ok && openaiProvider.SupportsModel(model) {
			return openaiProvider, "last success", nil
		}
	}

	// Find all OpenAI-compatible providers that support this model
	var candidates []provider.OpenAICompatibleProvider
	for _, pool := range pm.openaiPools {
		for _, p := range pool {
			if p.SupportsModel(model) {
				candidates = append(candidates, p)
			}
		}
	}

	if len(candidates) == 0 {
		return nil, "", fmt.Errorf("no OpenAI-compatible providers found for model: %s", model)
	}

	// Use smart selection with failure tracking
	selected := pm.selectOpenAIProviderWithFailureTracking(candidates, "openai-model:"+model)
	return selected, "round-robin", nil
}

// GetGeminiProvider returns a Gemini-native provider for the given type and model
func (pm *PoolManager) GetGeminiProvider(providerType string, model string) (provider.GeminiNativeProvider, error) {
	pool, ok := pm.geminiPools[providerType]
	if !ok || len(pool) == 0 {
		return nil, fmt.Errorf("no Gemini-native providers available for type: %s", providerType)
	}

	// Select provider based on rate limit rejection time
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	// Build list of candidates with their last rejection time
	type candidate struct {
		provider      provider.GeminiNativeProvider
		lastReject    time.Time
	}
	
	var candidates []candidate
	for _, p := range pool {
		key := p.Name() + ":" + model
		lastReject := pm.rateLimitRejectTime[key]
		candidates = append(candidates, candidate{
			provider:   p,
			lastReject: lastReject,
		})
	}

	// Select the provider with the oldest rejection time (or zero if never rejected)
	selected := candidates[0].provider
	oldestReject := candidates[0].lastReject

	for _, c := range candidates[1:] {
		if c.lastReject.Before(oldestReject) {
			oldestReject = c.lastReject
			selected = c.provider
		} else if c.lastReject.Equal(oldestReject) {
			// If multiple providers have the same rejection time, randomly select one
			if pm.rng.Intn(2) == 0 {
				selected = c.provider
			}
		}
	}

	utils.L().Debugw("Selected Gemini provider based on rate limit rejection time",
		"provider_type", providerType,
		"model", model,
		"selected_provider", selected.Name(),
		"last_reject", oldestReject.Format(time.RFC3339))

	return selected, nil
}

// GetKiroProvider returns a Kiro-native provider for the given type
func (pm *PoolManager) GetKiroProvider(providerType string) (provider.KiroNativeProvider, error) {
	pool, ok := pm.kiroPools[providerType]
	if !ok || len(pool) == 0 {
		return nil, fmt.Errorf("no Kiro-native providers available for type: %s", providerType)
	}

	// For now, return the first provider. Could add load balancing later.
	return pool[0], nil
}

// RecordSuccess records a successful request for a model
func (pm *PoolManager) RecordSuccess(model string, p provider.BaseProvider) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.lastSuccess[model] = p
	pm.failureCount[p.Name()] = 0
}

// RecordFailure records a failed request for a provider
func (pm *PoolManager) RecordFailure(p provider.BaseProvider) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.failureCount[p.Name()]++
}

// RecordRateLimitReject records a 429 rate limit rejection for a specific provider-model combination
func (pm *PoolManager) RecordRateLimitReject(providerName string, model string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	key := providerName + ":" + model
	pm.rateLimitRejectTime[key] = time.Now()
	utils.L().Infow("Recorded 429 rate limit rejection",
		"provider", providerName,
		"model", model,
		"reject_time", pm.rateLimitRejectTime[key].Format(time.RFC3339))
}

// RecordQuotaExhausted records a 429 quota exhaustion with explicit reset time for a specific provider-model combination
func (pm *PoolManager) RecordQuotaExhausted(providerName string, model string, resetTime time.Time) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	key := providerName + ":" + model
	pm.quotaExhaustedResetTime[key] = resetTime
	utils.L().Infow("Recorded 429 quota exhaustion with reset time",
		"provider", providerName,
		"model", model,
		"reset_time", resetTime.Format(time.RFC3339),
		"reset_in", time.Until(resetTime).String())
}

// ListModels returns a list of all models from OpenAI-compatible providers
func (pm *PoolManager) ListModels(ctx context.Context) ([]string, error) {
	uniqueModels := make(map[string]bool)
	var models []string

	for _, pool := range pm.openaiPools {
		if len(pool) > 0 {
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

// ListOpenAIProviderModels returns models for a specific OpenAI-compatible provider type
func (pm *PoolManager) ListOpenAIProviderModels(ctx context.Context, providerType string) ([]string, error) {
	pool, ok := pm.openaiPools[providerType]
	if !ok || len(pool) == 0 {
		return nil, fmt.Errorf("no OpenAI-compatible providers available for type: %s", providerType)
	}

	return pool[0].ListModels(ctx)
}

// ListGeminiProviderModels returns models for a specific Gemini-native provider type
func (pm *PoolManager) ListGeminiProviderModels(ctx context.Context, providerType string) ([]string, error) {
	pool, ok := pm.geminiPools[providerType]
	if !ok || len(pool) == 0 {
		return nil, fmt.Errorf("no Gemini-native providers available for type: %s", providerType)
	}

	return pool[0].ListModels(ctx)
}

// ListGeminiModels returns a list of all models from all Gemini-native providers
func (pm *PoolManager) ListGeminiModels(ctx context.Context) ([]string, error) {
	uniqueModels := make(map[string]bool)
	var models []string

	for _, pool := range pm.geminiPools {
		if len(pool) > 0 {
			p := pool[0]
			ms, err := p.ListModels(ctx)
			if err != nil {
				utils.L().Warnf("Failed to list models from Gemini provider %s: %v", p.Name(), err)
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

// GetAnyGeminiProviderByModel finds any Gemini provider that supports the given model
func (pm *PoolManager) GetAnyGeminiProviderByModel(model string) (provider.GeminiNativeProvider, error) {
	// Collect all providers that support this model
	type candidate struct {
		provider      provider.GeminiNativeProvider
		lastReject    time.Time
	}
	
	now := time.Now()
	var candidates []candidate
	var excludedProviders []string
	
	for _, pool := range pm.geminiPools {
		for _, p := range pool {
			if p.SupportsModel(model) {
				pm.mu.RLock()
				key := p.Name() + ":" + model
				
				// Check if this provider has active quota exhaustion
				if resetTime, ok := pm.quotaExhaustedResetTime[key]; ok && resetTime.After(now) {
					excludedProviders = append(excludedProviders, fmt.Sprintf("%s (quota resets in %s)", p.Name(), time.Until(resetTime).Round(time.Second)))
					pm.mu.RUnlock()
					continue
				}
				
				lastReject := pm.rateLimitRejectTime[key]
				pm.mu.RUnlock()
				candidates = append(candidates, candidate{
					provider:   p,
					lastReject: lastReject,
				})
			}
		}
	}

	if len(candidates) == 0 {
		if len(excludedProviders) > 0 {
			return nil, fmt.Errorf("no available Gemini-native providers for model %s (all excluded due to quota exhaustion: %s)", model, strings.Join(excludedProviders, ", "))
		}
		return nil, fmt.Errorf("no Gemini-native providers found for model: %s", model)
	}

	// Select the provider with the oldest rejection time (or zero if never rejected)
	selected := candidates[0].provider
	oldestReject := candidates[0].lastReject

	for _, c := range candidates[1:] {
		if c.lastReject.Before(oldestReject) {
			oldestReject = c.lastReject
			selected = c.provider
		} else if c.lastReject.Equal(oldestReject) {
			// If multiple providers have the same rejection time, randomly select one
			if pm.rng.Intn(2) == 0 {
				selected = c.provider
			}
		}
	}

	utils.L().Debugw("Selected Gemini provider (any) based on rate limit rejection time",
		"model", model,
		"selected_provider", selected.Name(),
		"last_reject", oldestReject.Format(time.RFC3339))

	return selected, nil
}