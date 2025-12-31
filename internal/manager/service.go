package manager

import (
	"context"
	"fmt"

	"github.com/sunbankio/omniproxy/internal/auth"
	"github.com/sunbankio/omniproxy/internal/provider"
)

// ProviderService is a facade that coordinates provider operations
type ProviderService struct {
	registry     *ProviderRegistry
	loadBalancer *LoadBalancer
}

// NewProviderService creates a new ProviderService
func NewProviderService(
	registry *ProviderRegistry,
	loadBalancer *LoadBalancer,
) *ProviderService {
	return &ProviderService{
		registry:     registry,
		loadBalancer: loadBalancer,
	}
}

// RecordSuccess records a successful request for a model
func (ps *ProviderService) RecordSuccess(model string, p provider.BaseProvider) {
	ps.loadBalancer.RecordSuccess(model, p)
}

// RecordFailure records a failed request for a provider
func (ps *ProviderService) RecordFailure(p provider.BaseProvider) {
	ps.loadBalancer.RecordFailure(p)
}

// ListModels returns a list of all models from OpenAI-compatible providers
func (ps *ProviderService) ListModels(ctx context.Context) ([]string, error) {
	uniqueModels := make(map[string]bool)
	var models []string

	// Iterate through all pools and get models from credentials
	pools := ps.registry.GetPools()
	for _, pool := range pools {
		credentials := pool.List()
		for _, cred := range credentials {
			// Check if this is an OpenAI-compatible provider
			if cred.ProviderType == auth.ProviderTypeOpenAI || cred.ProviderType == auth.ProviderTypeQwen || cred.ProviderType == auth.ProviderTypeIFlow {
				ms, err := cred.ListModels(ctx)
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
		}
	}
	return models, nil
}

// ListOpenAIProviderModels returns models for a specific OpenAI-compatible provider type
func (ps *ProviderService) ListOpenAIProviderModels(ctx context.Context, providerType provider.ProviderType) ([]string, error) {
	pool := ps.registry.GetPool(providerType)
	if pool == nil || pool.Size() == 0 {
		return nil, fmt.Errorf("no OpenAI-compatible providers available for type: %s", providerType)
	}

	credentials := pool.List()
	// Return models from the first credential
	if len(credentials) > 0 {
		return credentials[0].ListModels(ctx)
	}

	return nil, fmt.Errorf("no credentials found for type: %s", providerType)
}

// ListGeminiProviderModels returns models for a specific Gemini-native provider type
func (ps *ProviderService) ListGeminiProviderModels(ctx context.Context, providerType provider.ProviderType) ([]string, error) {
	pool := ps.registry.GetPool(providerType)
	if pool == nil || pool.Size() == 0 {
		return nil, fmt.Errorf("no Gemini-native providers available for type: %s", providerType)
	}

	credentials := pool.List()
	// Return models from the first credential
	if len(credentials) > 0 {
		return credentials[0].ListModels(ctx)
	}

	return nil, fmt.Errorf("no credentials found for type: %s", providerType)
}

// ListGeminiModels returns a list of all models from all Gemini-native providers
func (ps *ProviderService) ListGeminiModels(ctx context.Context) ([]string, error) {
	uniqueModels := make(map[string]bool)
	var models []string

	// Iterate through all pools and get models from credentials
	pools := ps.registry.GetPools()
	for _, pool := range pools {
		credentials := pool.List()
		for _, cred := range credentials {
			// Check if this is a Gemini-native provider
			if cred.ProviderType == auth.ProviderTypeGemini || cred.ProviderType == auth.ProviderTypeAntigravity {
				ms, err := cred.ListModels(ctx)
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
		}
	}
	return models, nil
}

// GetRegistry returns the provider registry
func (ps *ProviderService) GetRegistry() *ProviderRegistry {
	return ps.registry
}

// GetLoadBalancer returns the load balancer
func (ps *ProviderService) GetLoadBalancer() *LoadBalancer {
	return ps.loadBalancer
}
