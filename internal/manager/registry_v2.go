package manager

import (
	"sync"
	"time"

	"github.com/sunbankio/omniproxy/internal/auth"
	"github.com/sunbankio/omniproxy/internal/router"
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

// GetCredential returns a credential for the specified model
// This implements the CredentialSelector interface
func (r *Registry) GetCredential(model string) interface{} {
	pool := r.GetPool(model)
	if pool == nil {
		return nil
	}
	return pool.GetNext()
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

// RecordError records an error for a credential and updates its state based on error type
// This implements the CredentialErrorRecorder interface
func (r *Registry) RecordError(credentialID string, model string, err error) {
	pool := r.GetPool(model)
	if pool == nil {
		return
	}

	// Find the credential by ID and record the error
	pool.mu.Lock()
	defer pool.mu.Unlock()

	for _, cred := range pool.credentials {
		if cred.ID == credentialID {
			// Check if it's a 429 rate limit error
			if router.IsRateLimitError(err) {
				resetTime := router.ExtractQuotaResetTime(err)

				if !resetTime.IsZero() {
					// Type 1 or Type 2: Explicit reset time provided
					// Set AvailableAt - credential won't be selected until this time
					cred.AvailableAt = resetTime
					cred.FailureCount = 0 // Reset failure count since we have explicit reset time
				} else {
					// Type 3: No explicit reset time - use exponential backoff
					cred.FailureCount++
					backoffDuration := router.GetExponentialBackoffDuration(cred.FailureCount)
					cred.AvailableAt = time.Now().Add(backoffDuration)
				}

				return
			}

			// For other errors, just increase failure count
			cred.FailureCount++
			if cred.FailureCount > 10 {
				// Mark as dead if too many failures
				cred.State = auth.CredentialStateDead
			}
			return
		}
	}
}

// RecordSuccess records a successful request for a credential
// This implements the CredentialErrorRecorder interface
func (r *Registry) RecordSuccess(credentialID string, model string) {
	pool := r.GetPool(model)
	if pool == nil {
		return
	}

	// Find the credential by ID and reset its failure count
	pool.mu.Lock()
	defer pool.mu.Unlock()

	for _, cred := range pool.credentials {
		if cred.ID == credentialID {
			// Reset FailureCount to 0 to prevent backoff accumulation
			cred.FailureCount = 0
			return
		}
	}
}

// MarkDead marks a credential as permanently dead
// This implements the CredentialErrorRecorder interface
func (r *Registry) MarkDead(credentialID string, model string) {
	pool := r.GetPool(model)
	if pool == nil {
		return
	}

	// Find the credential by ID and mark it as dead
	pool.mu.Lock()
	defer pool.mu.Unlock()

	for _, cred := range pool.credentials {
		if cred.ID == credentialID {
			cred.State = auth.CredentialStateDead
			return
		}
	}
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

// GetNext returns a credential based on round-robin selection.
// Implements the following logic:
// 1. Round-robin selection based on LastUsedAt timestamp
// 2. Skip credentials that are in rate limit penalty (AvailableAt > now)
// 3. Skip credentials that are dead
// 4. Update LastUsedAt when a credential is selected
func (p *CredentialPool) GetNext() *auth.Credential {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.credentials) == 0 {
		return nil
	}

	now := time.Now()
	var selectedCred *auth.Credential
	var oldestLastUsed time.Time

	// Find the oldest used credential that is not in penalty
	for _, cred := range p.credentials {
		// Check if credential is in rate limit penalty
		if !cred.AvailableAt.IsZero() && now.Before(cred.AvailableAt) {
			// Skip this credential - it's in rate limit penalty
			continue
		}

		// Check if credential is dead
		if cred.State == auth.CredentialStateDead {
			continue
		}

		// Track the oldest used credential for round-robin
		if selectedCred == nil || cred.LastUsedAt.Before(oldestLastUsed) {
			selectedCred = cred
			oldestLastUsed = cred.LastUsedAt
		}
	}

	// If no healthy credential found, return nil
	if selectedCred == nil {
		return nil
	}

	// Update LastUsedAt for round-robin
	selectedCred.LastUsedAt = now

	// Clear AvailableAt if it has passed to keep credential object clean
	if !selectedCred.AvailableAt.IsZero() && now.After(selectedCred.AvailableAt) {
		selectedCred.AvailableAt = time.Time{}
	}

	return selectedCred
}
