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
					// Set RateLimitResetTime - credential won't be selected until this time
					cred.RateLimitResetTime = resetTime
					cred.FailureCount = 0 // Reset failure count since we have explicit reset time
				} else {
					// Type 3: No explicit reset time - use exponential backoff
					cred.FailureCount++
					backoffDuration := router.GetExponentialBackoffDuration(cred.FailureCount)
					cred.RateLimitResetTime = time.Now().Add(backoffDuration)
				}

				// Log the rate limit error handling
				// utils.L().Warnw("Rate limit error recorded",
				// 	"credential_id", cred.ID,
				// 	"failure_count", cred.FailureCount,
				// 	"rate_limit_reset_time", cred.RateLimitResetTime,
				// 	"error", err)
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
// 2. Skip credentials that are in rate limit penalty (RateLimitResetTime > now)
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
		if !cred.RateLimitResetTime.IsZero() && now.Before(cred.RateLimitResetTime) {
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

	// Clear RateLimitResetTime if it has passed to keep credential object clean
	if !selectedCred.RateLimitResetTime.IsZero() && now.After(selectedCred.RateLimitResetTime) {
		selectedCred.RateLimitResetTime = time.Time{}
	}

	return selectedCred
}
