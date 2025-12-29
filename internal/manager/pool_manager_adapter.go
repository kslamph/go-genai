package manager

import (
	"context"

	"github.com/sunbankio/omniproxy/internal/config"
)

// NewProviderServiceManager creates a new ProviderService and initializes providers
func NewProviderServiceManager(ctx context.Context, cfg *config.Config) (*ProviderService, error) {
	// Create the new components
	registry := NewProviderRegistry()
	loadBalancer := NewLoadBalancer()
	rateLimitTracker := NewRateLimitTracker()
	
	// Create the service
	service := NewProviderService(registry, loadBalancer, rateLimitTracker)
	
	// Initialize providers and register them using the factory
	factory := NewProviderFactory()
	if err := factory.InitializeAllProviders(ctx, cfg, registry); err != nil {
		return nil, err
	}
	
	return service, nil
}
