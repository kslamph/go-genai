package manager

import (
	"sync"

	"github.com/sunbankio/omniproxy/internal/provider"
)

// ProviderRegistry stores and provides access to all provider instances
type ProviderRegistry struct {
	// pools maps a ProviderType to a Pool of providers
	pools map[provider.ProviderType]*Pool

	// mu protects the pools map
	mu sync.RWMutex
}

// NewProviderRegistry creates a new empty registry
func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{
		pools: make(map[provider.ProviderType]*Pool),
	}
}

// getOrCreatePool returns an existing pool or creates a new one safely
func (r *ProviderRegistry) getOrCreatePool(t provider.ProviderType) *Pool {
	r.mu.Lock()
	defer r.mu.Unlock()
	
	if pool, exists := r.pools[t]; exists {
		return pool
	}
	
	pool := NewPool()
	r.pools[t] = pool
	return pool
}

// GetPool returns a pool if it exists, safely
func (r *ProviderRegistry) GetPool(t provider.ProviderType) *Pool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.pools[t]
}

// GetPools returns all active pools
func (r *ProviderRegistry) GetPools() []*Pool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var pools []*Pool
	for _, pool := range r.pools {
		pools = append(pools, pool)
	}
	return pools
}

// RegisterOpenAIProvider adds an OpenAI-compatible provider to the registry
func (r *ProviderRegistry) RegisterOpenAIProvider(providerType provider.ProviderType, p provider.OpenAICompatibleProvider) {
	pool := r.getOrCreatePool(providerType)
	pool.Add(p)
}

// RegisterGeminiProvider adds a Gemini-native provider to the registry
func (r *ProviderRegistry) RegisterGeminiProvider(providerType provider.ProviderType, p provider.GeminiNativeProvider) {
	pool := r.getOrCreatePool(providerType)
	pool.Add(p)
}

// GetOpenAIProviders returns all OpenAI-compatible providers of a given type
func (r *ProviderRegistry) GetOpenAIProviders(providerType provider.ProviderType) []provider.OpenAICompatibleProvider {
	pool := r.GetPool(providerType)
	if pool == nil {
		return nil
	}

	var providers []provider.OpenAICompatibleProvider
	for _, p := range pool.List() {
		if prov, ok := p.(provider.OpenAICompatibleProvider); ok {
			providers = append(providers, prov)
		}
	}
	return providers
}

// GetGeminiProviders returns all Gemini-native providers of a given type
func (r *ProviderRegistry) GetGeminiProviders(providerType provider.ProviderType) []provider.GeminiNativeProvider {
	pool := r.GetPool(providerType)
	if pool == nil {
		return nil
	}

	var providers []provider.GeminiNativeProvider
	for _, p := range pool.List() {
		if prov, ok := p.(provider.GeminiNativeProvider); ok {
			providers = append(providers, prov)
		}
	}
	return providers
}

// GetAllOpenAIProviders returns all OpenAI-compatible providers from all types
func (r *ProviderRegistry) GetAllOpenAIProviders() []provider.OpenAICompatibleProvider {
	r.mu.RLock()
	// We need to copy the map keys/values to avoid holding the lock while iterating pools if we were doing complex logic,
	// but here we just iterate the map.
	// However, pool.List() acquires the pool's lock, so we must be careful about deadlocks if we held registry lock.
	// But r.mu protects the map itself.
	
	// Better approach: Snapshot the pools
	var allPools []*Pool
	for _, pool := range r.pools {
		allPools = append(allPools, pool)
	}
	r.mu.RUnlock()

	var providers []provider.OpenAICompatibleProvider
	for _, pool := range allPools {
		for _, p := range pool.List() {
			if prov, ok := p.(provider.OpenAICompatibleProvider); ok {
				providers = append(providers, prov)
			}
		}
	}
	return providers
}

// GetAllGeminiProviders returns all Gemini-native providers from all types
func (r *ProviderRegistry) GetAllGeminiProviders() []provider.GeminiNativeProvider {
	r.mu.RLock()
	var allPools []*Pool
	for _, pool := range r.pools {
		allPools = append(allPools, pool)
	}
	r.mu.RUnlock()

	var providers []provider.GeminiNativeProvider
	for _, pool := range allPools {
		for _, p := range pool.List() {
			if prov, ok := p.(provider.GeminiNativeProvider); ok {
				providers = append(providers, prov)
			}
		}
	}
	return providers
}

// RemoveProvider removes a specific provider from the registry
func (r *ProviderRegistry) RemoveProvider(p provider.BaseProvider) {
	// We don't need to switch type, just find the pool for this provider type
	pool := r.GetPool(p.Type())
	if pool != nil {
		pool.Remove(p.Name())
	}
}
