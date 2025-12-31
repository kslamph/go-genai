package manager

import (
	"sync"

	"github.com/sunbankio/omniproxy/internal/auth"
)

// Pool manages a collection of active Credentials for a specific service type.
// It acts as a container that handles thread-safe access to the credentials.
type Pool struct {
	// credentials holds the active credential instances
	credentials []*auth.Credential

	// mu protects the credentials slice
	mu sync.RWMutex
}

// NewPool creates a new empty Pool
func NewPool() *Pool {
	return &Pool{
		credentials: make([]*auth.Credential, 0),
	}
}

// Add adds a credential to the pool
func (p *Pool) Add(cred *auth.Credential) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.credentials = append(p.credentials, cred)
}

// Remove removes a credential from the pool by its name
func (p *Pool) Remove(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i, cred := range p.credentials {
		if cred.Name() == name {
			// Efficient removal by swapping with last element (order doesn't matter for pool)
			// or standard slice removal to maintain order.
			// Standard removal to maintain stable order for RoundRobin if needed.
			p.credentials = append(p.credentials[:i], p.credentials[i+1:]...)
			return
		}
	}
}

// List returns a copy of the slice of all credentials in the pool
func (p *Pool) List() []*auth.Credential {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// Return a copy to prevent race conditions on the slice if the caller iterates
	result := make([]*auth.Credential, len(p.credentials))
	copy(result, p.credentials)
	return result
}

// Size returns the number of credentials in the pool
func (p *Pool) Size() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.credentials)
}

// Get returns a specific credential by name, or nil if not found
func (p *Pool) Get(name string) *auth.Credential {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, cred := range p.credentials {
		if cred.Name() == name {
			return cred
		}
	}
	return nil
}
