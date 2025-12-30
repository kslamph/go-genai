package manager

import (
	"sync"

	"github.com/sunbankio/omniproxy/internal/provider"
)

// Pool manages a collection of active Providers for a specific service type.
// It acts as a container that handles thread-safe access to the providers.
type Pool struct {
	// providers holds the active provider instances
	providers []provider.BaseProvider
	
	// mu protects the providers slice
	mu sync.RWMutex
}

// NewPool creates a new empty Pool
func NewPool() *Pool {
	return &Pool{
		providers: make([]provider.BaseProvider, 0),
	}
}

// Add adds a provider to the pool
func (p *Pool) Add(prov provider.BaseProvider) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.providers = append(p.providers, prov)
}

// Remove removes a provider from the pool by its name
func (p *Pool) Remove(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	
	for i, prov := range p.providers {
		if prov.Name() == name {
			// Efficient removal by swapping with last element (order doesn't matter for pool)
			// or standard slice removal to maintain order. 
			// Standard removal to maintain stable order for RoundRobin if needed.
			p.providers = append(p.providers[:i], p.providers[i+1:]...)
			return
		}
	}
}

// List returns a copy of the slice of all providers in the pool
func (p *Pool) List() []provider.BaseProvider {
	p.mu.RLock()
	defer p.mu.RUnlock()
	
	// Return a copy to prevent race conditions on the slice if the caller iterates
	result := make([]provider.BaseProvider, len(p.providers))
	copy(result, p.providers)
	return result
}

// Size returns the number of providers in the pool
func (p *Pool) Size() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.providers)
}

// Get returns a specific provider by name, or nil if not found
func (p *Pool) Get(name string) provider.BaseProvider {
	p.mu.RLock()
	defer p.mu.RUnlock()
	
	for _, prov := range p.providers {
		if prov.Name() == name {
			return prov
		}
	}
	return nil
}
