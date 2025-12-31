package manager

import (
	"sync"

	"github.com/sunbankio/omniproxy/internal/auth"
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
// TODO: This method should be updated to accept Credential instead of provider
// For now, we create a credential wrapper around the provider
func (r *ProviderRegistry) RegisterOpenAIProvider(providerType provider.ProviderType, p provider.OpenAICompatibleProvider) {
	// Create a credential from the provider with the correct provider type
	// This is a temporary bridge until the provider factory creates credentials directly
	var authProviderType auth.ProviderType
	switch providerType {
	case provider.ProviderQwen:
		authProviderType = auth.ProviderTypeQwen
	case provider.ProviderIFlow:
		authProviderType = auth.ProviderTypeIFlow
	case provider.ProviderOpenAI:
		authProviderType = auth.ProviderTypeOpenAI
	default:
		authProviderType = auth.ProviderTypeOpenAI // fallback
	}

	cred := auth.NewCredential(p.Name(), authProviderType)

	// Store the provider instance in the credential
	cred.SetProvider(p)

	pool := r.getOrCreatePool(providerType)
	pool.Add(cred)
}

// RegisterGeminiProvider adds a Gemini-native provider to the registry
// TODO: This method should be updated to accept Credential instead of provider
// For now, we create a credential wrapper around the provider
func (r *ProviderRegistry) RegisterGeminiProvider(providerType provider.ProviderType, p provider.GeminiNativeProvider) {
	// Create a credential from the provider with the correct provider type
	// This is a temporary bridge until the provider factory creates credentials directly
	var authProviderType auth.ProviderType
	switch providerType {
	case provider.ProviderGemini:
		authProviderType = auth.ProviderTypeGemini
	case provider.ProviderAntigravity:
		authProviderType = auth.ProviderTypeAntigravity
	default:
		authProviderType = auth.ProviderTypeGemini // fallback
	}

	cred := auth.NewCredential(p.Name(), authProviderType)

	// Store the provider instance in the credential
	cred.SetProvider(p)

	pool := r.getOrCreatePool(providerType)
	pool.Add(cred)
}

// RemoveProvider removes a specific provider from the registry
func (r *ProviderRegistry) RemoveProvider(p provider.BaseProvider) {
	// We don't need to switch type, just find the pool for this provider type
	pool := r.GetPool(p.Type())
	if pool != nil {
		pool.Remove(p.Name())
	}
}

// RemoveCredential removes a specific credential from the registry
func (r *ProviderRegistry) RemoveCredential(cred *auth.Credential) {
	// Find the pool for this credential type
	pool := r.GetPool(cred.Type())
	if pool != nil {
		pool.Remove(cred.Name())
	}
}

// GetCredentials returns all credentials for a given provider type
func (r *ProviderRegistry) GetCredentials(providerType provider.ProviderType) []*auth.Credential {
	pool := r.GetPool(providerType)
	if pool == nil {
		return nil
	}
	return pool.List()
}
