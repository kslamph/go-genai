package manager

import (
	"context"
	"fmt"
	"time"

	"github.com/sunbankio/omniproxy/internal/provider"
)

// ProviderService is a facade that coordinates provider operations
type ProviderService struct {
	registry        *ProviderRegistry
	loadBalancer    *LoadBalancer
	rateLimitTracker *RateLimitTracker
}

// NewProviderService creates a new ProviderService
func NewProviderService(
	registry *ProviderRegistry,
	loadBalancer *LoadBalancer,
	rateLimitTracker *RateLimitTracker,
) *ProviderService {
	return &ProviderService{
		registry:         registry,
		loadBalancer:     loadBalancer,
		rateLimitTracker: rateLimitTracker,
	}
}

// GetOpenAIProvider returns an OpenAI-compatible provider for the given type and model
func (ps *ProviderService) GetOpenAIProvider(providerType provider.ProviderType, model string) (provider.OpenAICompatibleProvider, error) {
	p, _, err := ps.GetOpenAIProviderWithReason(providerType, model)
	return p, err
}

// GetOpenAIProviderWithReason returns an OpenAI-compatible provider with selection reason
func (ps *ProviderService) GetOpenAIProviderWithReason(providerType provider.ProviderType, model string) (provider.OpenAICompatibleProvider, string, error) {
	// Check if last successful provider for this model matches the type and is OpenAI-compatible
	last := ps.loadBalancer.GetLastSuccess(model)
	if last != nil {
		if openaiProvider, ok := last.(provider.OpenAICompatibleProvider); ok && openaiProvider.Type() == providerType {
			return openaiProvider, "last success", nil
		}
	}

	// Otherwise, select using smart round-robin with failure tracking
	pool := ps.registry.GetOpenAIProviders(providerType)
	if len(pool) == 0 {
		return nil, "", fmt.Errorf("no OpenAI-compatible providers available for type: %s", providerType)
	}

	selected := ps.loadBalancer.SelectOpenAIProvider(pool, providerType)
	return selected, "round-robin", nil
}

// GetOpenAIProviderByModel finds an OpenAI-compatible provider by model name
func (ps *ProviderService) GetOpenAIProviderByModel(model string) (provider.OpenAICompatibleProvider, error) {
	p, _, err := ps.GetOpenAIProviderByModelWithReason(model)
	return p, err
}

// GetOpenAIProviderByModelWithReason finds an OpenAI-compatible provider by model name with reason
func (ps *ProviderService) GetOpenAIProviderByModelWithReason(model string) (provider.OpenAICompatibleProvider, string, error) {
	// Check if last successful provider still supports this model and is OpenAI-compatible
	last := ps.loadBalancer.GetLastSuccess(model)
	if last != nil {
		if openaiProvider, ok := last.(provider.OpenAICompatibleProvider); ok && openaiProvider.SupportsModel(model) {
			return openaiProvider, "last success", nil
		}
	}

	// Find all OpenAI-compatible providers that support this model
	allProviders := ps.registry.GetAllOpenAIProviders()
	var candidates []provider.OpenAICompatibleProvider
	for _, p := range allProviders {
		if p.SupportsModel(model) {
			candidates = append(candidates, p)
		}
	}

	if len(candidates) == 0 {
		return nil, "", fmt.Errorf("no OpenAI-compatible providers found for model: %s", model)
	}

	// Use smart selection with failure tracking
	// For model-based selection, we use a special pool key
	selected := ps.loadBalancer.SelectOpenAIProvider(candidates, provider.ProviderType("model:"+model))
	return selected, "round-robin", nil
}

// GetGeminiProvider returns a Gemini-native provider for the given type and model
func (ps *ProviderService) GetGeminiProvider(providerType provider.ProviderType, model string) (provider.GeminiNativeProvider, error) {
	pool := ps.registry.GetGeminiProviders(providerType)
	if len(pool) == 0 {
		return nil, fmt.Errorf("no Gemini-native providers available for type: %s", providerType)
	}

	// Select provider based on rate limit rejection time
	selected := ps.loadBalancer.SelectGeminiProvider(pool, model, ps.rateLimitTracker)
	return selected, nil
}

// RecordSuccess records a successful request for a model
func (ps *ProviderService) RecordSuccess(model string, p provider.BaseProvider) {
	ps.loadBalancer.RecordSuccess(model, p)
}

// RecordFailure records a failed request for a provider
func (ps *ProviderService) RecordFailure(p provider.BaseProvider) {
	ps.loadBalancer.RecordFailure(p)
}

// RecordRateLimitReject records a 429 rate limit rejection for a specific provider-model combination
func (ps *ProviderService) RecordRateLimitReject(providerName string, model string) {
	ps.rateLimitTracker.RecordRateLimitReject(providerName, model)
}

// RecordQuotaExhausted records a 429 quota exhaustion with explicit reset time for a specific provider-model combination
func (ps *ProviderService) RecordQuotaExhausted(providerName string, model string, resetTime time.Time) {
	ps.rateLimitTracker.RecordQuotaExhausted(providerName, model, resetTime)
}

// ListModels returns a list of all models from OpenAI-compatible providers
func (ps *ProviderService) ListModels(ctx context.Context) ([]string, error) {
	uniqueModels := make(map[string]bool)
	var models []string

	allProviders := ps.registry.GetAllOpenAIProviders()
	for _, p := range allProviders {
		ms, err := p.ListModels(ctx)
		if err != nil {
			continue
		}
		for _, m := range ms {
			if !uniqueModels[m] {
				uniqueModels[m] = true
				models = append(models, m)
			}
		}
	}
	return models, nil
}

// ListOpenAIProviderModels returns models for a specific OpenAI-compatible provider type
func (ps *ProviderService) ListOpenAIProviderModels(ctx context.Context, providerType provider.ProviderType) ([]string, error) {
	pool := ps.registry.GetOpenAIProviders(providerType)
	if len(pool) == 0 {
		return nil, fmt.Errorf("no OpenAI-compatible providers available for type: %s", providerType)
	}

	return pool[0].ListModels(ctx)
}

// ListGeminiProviderModels returns models for a specific Gemini-native provider type
func (ps *ProviderService) ListGeminiProviderModels(ctx context.Context, providerType provider.ProviderType) ([]string, error) {
	pool := ps.registry.GetGeminiProviders(providerType)
	if len(pool) == 0 {
		return nil, fmt.Errorf("no Gemini-native providers available for type: %s", providerType)
	}

	return pool[0].ListModels(ctx)
}

// ListGeminiModels returns a list of all models from all Gemini-native providers
func (ps *ProviderService) ListGeminiModels(ctx context.Context) ([]string, error) {
	uniqueModels := make(map[string]bool)
	var models []string

	allProviders := ps.registry.GetAllGeminiProviders()
	for _, p := range allProviders {
		ms, err := p.ListModels(ctx)
		if err != nil {
			continue
		}
		for _, m := range ms {
			if !uniqueModels[m] {
				uniqueModels[m] = true
				models = append(models, m)
			}
		}
	}
	return models, nil
}

// GetAnyGeminiProviderByModel finds any Gemini provider that supports the given model
func (ps *ProviderService) GetAnyGeminiProviderByModel(model string) (provider.GeminiNativeProvider, error) {
	// Collect all providers that support this model
	allProviders := ps.registry.GetAllGeminiProviders()
	var candidates []provider.GeminiNativeProvider
	
	for _, p := range allProviders {
		if p.SupportsModel(model) {
			candidates = append(candidates, p)
		}
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no Gemini providers found for model: %s", model)
	}

	// Select the best candidate based on rate limit tracking
	selected := ps.loadBalancer.SelectGeminiProvider(candidates, model, ps.rateLimitTracker)
	return selected, nil
}
