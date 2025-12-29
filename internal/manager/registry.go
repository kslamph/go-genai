package manager

import (
	"sync"

	"github.com/sunbankio/omniproxy/internal/provider"
)

// ProviderRegistry stores and provides access to all provider instances
type ProviderRegistry struct {
	// OpenAI-compatible providers (qwen, iflow)
	openaiPools map[string][]provider.OpenAICompatibleProvider

	// Gemini-native providers (gemini, antigravity)
	geminiPools map[string][]provider.GeminiNativeProvider

	// Kiro-native providers
	kiroPools map[string][]provider.KiroNativeProvider

	// mu protects all provider pools
	mu sync.RWMutex
}

// NewProviderRegistry creates a new empty registry
func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{
		openaiPools: make(map[string][]provider.OpenAICompatibleProvider),
		geminiPools: make(map[string][]provider.GeminiNativeProvider),
		kiroPools:   make(map[string][]provider.KiroNativeProvider),
	}
}

// RegisterOpenAIProvider adds an OpenAI-compatible provider to the registry
func (r *ProviderRegistry) RegisterOpenAIProvider(providerType string, p provider.OpenAICompatibleProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.openaiPools[providerType] = append(r.openaiPools[providerType], p)
}

// RegisterGeminiProvider adds a Gemini-native provider to the registry
func (r *ProviderRegistry) RegisterGeminiProvider(providerType string, p provider.GeminiNativeProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.geminiPools[providerType] = append(r.geminiPools[providerType], p)
}

// RegisterKiroProvider adds a Kiro-native provider to the registry
func (r *ProviderRegistry) RegisterKiroProvider(providerType string, p provider.KiroNativeProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kiroPools[providerType] = append(r.kiroPools[providerType], p)
}

// GetOpenAIProviders returns all OpenAI-compatible providers of a given type
func (r *ProviderRegistry) GetOpenAIProviders(providerType string) []provider.OpenAICompatibleProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.openaiPools[providerType]
}

// GetGeminiProviders returns all Gemini-native providers of a given type
func (r *ProviderRegistry) GetGeminiProviders(providerType string) []provider.GeminiNativeProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.geminiPools[providerType]
}

// GetKiroProviders returns all Kiro-native providers of a given type
func (r *ProviderRegistry) GetKiroProviders(providerType string) []provider.KiroNativeProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.kiroPools[providerType]
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