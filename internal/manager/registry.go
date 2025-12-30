package manager

import (
	"sync"

	"github.com/sunbankio/omniproxy/internal/provider"
)

// ProviderRegistry stores and provides access to all provider instances
type ProviderRegistry struct {
	// OpenAI-compatible providers (qwen, iflow)
	openaiPools map[provider.ProviderType][]provider.OpenAICompatibleProvider

	// Gemini-native providers (gemini, antigravity)
	geminiPools map[provider.ProviderType][]provider.GeminiNativeProvider

	// mu protects all provider pools
	mu sync.RWMutex
}

// NewProviderRegistry creates a new empty registry
func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{
		openaiPools: make(map[provider.ProviderType][]provider.OpenAICompatibleProvider),
		geminiPools: make(map[provider.ProviderType][]provider.GeminiNativeProvider),
	}
}

// RegisterOpenAIProvider adds an OpenAI-compatible provider to the registry
func (r *ProviderRegistry) RegisterOpenAIProvider(providerType provider.ProviderType, p provider.OpenAICompatibleProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.openaiPools[providerType] = append(r.openaiPools[providerType], p)
}

// RegisterGeminiProvider adds a Gemini-native provider to the registry
func (r *ProviderRegistry) RegisterGeminiProvider(providerType provider.ProviderType, p provider.GeminiNativeProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.geminiPools[providerType] = append(r.geminiPools[providerType], p)
}

// GetOpenAIProviders returns all OpenAI-compatible providers of a given type
func (r *ProviderRegistry) GetOpenAIProviders(providerType provider.ProviderType) []provider.OpenAICompatibleProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.openaiPools[providerType]
}

// GetGeminiProviders returns all Gemini-native providers of a given type
func (r *ProviderRegistry) GetGeminiProviders(providerType provider.ProviderType) []provider.GeminiNativeProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.geminiPools[providerType]
}

// GetAllOpenAIProviders returns all OpenAI-compatible providers from all types
func (r *ProviderRegistry) GetAllOpenAIProviders() []provider.OpenAICompatibleProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	
	var providers []provider.OpenAICompatibleProvider
	for _, pool := range r.openaiPools {
		providers = append(providers, pool...)
	}
	return providers
}

// GetAllGeminiProviders returns all Gemini-native providers from all types
func (r *ProviderRegistry) GetAllGeminiProviders() []provider.GeminiNativeProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var providers []provider.GeminiNativeProvider
	for _, pool := range r.geminiPools {
		providers = append(providers, pool...)
	}
	return providers
}

// RemoveProvider removes a specific provider from the registry
func (r *ProviderRegistry) RemoveProvider(p provider.BaseProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()

	switch prov := p.(type) {
	case provider.GeminiNativeProvider:
		// Remove from Gemini pools
		if pool, ok := r.geminiPools[p.Type()]; ok {
			for i, providerInPool := range pool {
				if providerInPool.Name() == prov.Name() {
					r.geminiPools[p.Type()] = append(pool[:i], pool[i+1:]...)
					break
				}
			}
		}
	case provider.OpenAICompatibleProvider:
		// Remove from OpenAI pools
		if pool, ok := r.openaiPools[p.Type()]; ok {
			for i, providerInPool := range pool {
				if providerInPool.Name() == prov.Name() {
					r.openaiPools[p.Type()] = append(pool[:i], pool[i+1:]...)
					break
				}
			}
		}
	}
}
