package manager

import (
	"sync"
	"time"

	"github.com/sunbankio/omniproxy/internal/auth"
)

// Registry stores the mapping between Virtual Models and Credentials
type Registry struct {
	// modelPools maps a Virtual Model Name (e.g., "gemini-1.5-pro") to a pool of credentials
	modelPools map[string]*CredentialPool

	// mu protects the map
	mu sync.RWMutex
}

// CredentialPool manages a list of credentials for a specific virtual model
type CredentialPool struct {
	credentials []*auth.Credential
	mu          sync.RWMutex
}

// NewRegistry creates a new empty Registry
func NewRegistry() *Registry {
	return &Registry{
		modelPools: make(map[string]*CredentialPool),
	}
}

// RegisterCredential adds a credential to the pool for the specified models
func (r *Registry) RegisterCredential(cred *auth.Credential, models []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, model := range models {
		pool, exists := r.modelPools[model]
		if !exists {
			pool = &CredentialPool{
				credentials: make([]*auth.Credential, 0),
			}
			r.modelPools[model] = pool
		}
		pool.Add(cred)
	}
}

// GetPool returns the credential pool for a given model
func (r *Registry) GetPool(model string) *CredentialPool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.modelPools[model]
}

// ListModels returns a list of all registered model names
func (r *Registry) ListModels() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	models := make([]string, 0, len(r.modelPools))
	for model := range r.modelPools {
		models = append(models, model)
	}
	return models
}

// Add adds a credential to the pool
func (p *CredentialPool) Add(cred *auth.Credential) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.credentials = append(p.credentials, cred)
}

// List returns a copy of the credentials in the pool
func (p *CredentialPool) List() []*auth.Credential {
	p.mu.RLock()
	defer p.mu.RUnlock()
	
	result := make([]*auth.Credential, len(p.credentials))
	copy(result, p.credentials)
	return result
}

// GetNext returns a credential based on a simple round-robin or first-available logic.
// This is a simplified selector that can be expanded later.
func (p *CredentialPool) GetNext() *auth.Credential {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if len(p.credentials) == 0 {
		return nil
	}

	// Simple selection: find the first Active credential not in penalty box
	// (Round-robin state would be tracked here if we wanted strict RR)
	for _, cred := range p.credentials {
		if cred.State == auth.CredentialStateActive {
			return cred
		}
		if cred.State == auth.CredentialStatePenaltyBox && time.Now().After(cred.PenaltyUntil) {
			// Auto-release from penalty box
			// Note: We need a write lock to change state, but we are in RLock.
			// Ideally this state management happens in the AuthManager or a background loop.
			// For now, we just skip it to be safe, or we could upgrade lock.
			// Let's just return it if the time has passed, letting the caller handle the state reset if needed.
			return cred
		}
	}

	// If all are penalized, return the one with earliest expiry?
	// Or just return nil/error?
	// For "fast fail", we might return nil if no healthy creds exist.
	return nil
}

// GetAllModels returns a list of all models registered in the registry
func (r *Registry) GetAllModels() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	models := make([]string, 0, len(r.modelPools))
	for model := range r.modelPools {
		models = append(models, model)
	}
	return models
}
