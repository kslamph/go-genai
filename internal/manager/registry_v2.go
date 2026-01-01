package manager

import (
	"strings"
	"sync"
	"time"

	"github.com/sunbankio/omniproxy/internal/auth"
	"github.com/sunbankio/omniproxy/pkg/utils"
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

// InternalPenaltyInfo contains information about rate limit penalties for credentials
// This is an internal type to avoid circular imports
type InternalPenaltyInfo struct {
	AllInPenalty      bool      // True if all credentials are in penalty
	ShortestResetTime time.Time // The earliest time when any credential will be available
}

// isRateLimitError checks if an error is a 429 rate limit error
func isRateLimitError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	return strings.Contains(errStr, "429") ||
		strings.Contains(errStr, "QUOTA_EXHAUSTED") ||
		strings.Contains(errStr, "RATE_LIMIT_EXCEEDED")
}

// extractQuotaResetTime extracts the quota reset time from a 429 error message
func extractQuotaResetTime(err error) time.Time {
	if err == nil {
		return time.Time{}
	}

	errStr := err.Error()

	// Check if this is a QUOTA_EXHAUSTED or RATE_LIMIT_EXCEEDED error
	if !strings.Contains(errStr, "QUOTA_EXHAUSTED") && !strings.Contains(errStr, "RATE_LIMIT_EXCEEDED") {
		return time.Time{}
	}

	// Type 1: "Your quota will reset after 4h53m59s." or "Your quota will reset after 49s."
	if strings.Contains(errStr, "Your quota will reset after") {
		parts := strings.Split(errStr, "Your quota will reset after")
		if len(parts) > 1 {
			resetPart := strings.TrimSpace(parts[1])
			// Extract the duration (find the first period or comma as delimiter)
			delimiters := []string{".", ",", "Status:"}
			durationStr := resetPart
			for _, delim := range delimiters {
				if idx := strings.Index(resetPart, delim); idx > 0 {
					durationStr = resetPart[:idx]
					break
				}
			}
			durationStr = strings.TrimSpace(durationStr)
			// Parse the duration using Go's time.ParseDuration (supports 4h53m59s, 49s, etc.)
			if duration, err := time.ParseDuration(durationStr); err == nil {
				return time.Now().Add(duration)
			}
		}
	}

	// Type 2: "retryDelay:17639.032973961s" or "quotaResetDelay:4h53m59.032973961s"
	// Extract from any *Delay: format
	delayPatterns := []string{"retryDelay:", "quotaResetDelay:"}
	for _, pattern := range delayPatterns {
		if idx := strings.Index(errStr, pattern); idx >= 0 {
			start := idx + len(pattern)
			end := strings.IndexAny(errStr[start:], ",.")
			if end == -1 {
				end = len(errStr)
			} else {
				end += start
			}
			durationStr := strings.TrimSpace(errStr[start:end])
			if duration, err := time.ParseDuration(durationStr); err == nil {
				return time.Now().Add(duration)
			}
		}
	}

	// Type 3: No explicit reset time - return zero time
	return time.Time{}
}

// getExponentialBackoffDuration calculates the exponential backoff duration based on failure count
func getExponentialBackoffDuration(failureCount int) time.Duration {
	backoffs := []time.Duration{
		30 * time.Second,
		1 * time.Minute,
		3 * time.Minute,
		5 * time.Minute,
		10 * time.Minute,
		30 * time.Minute,
		60 * time.Minute,
	}

	if failureCount <= 0 {
		return backoffs[0]
	}

	if failureCount-1 < len(backoffs) {
		return backoffs[failureCount-1]
	}

	// Cap at 60 minutes for high failure counts
	return backoffs[len(backoffs)-1]
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

// GetCredentialWithPenaltyInfo returns a credential and penalty information
// This implements the CredentialSelector interface
func (r *Registry) GetCredentialWithPenaltyInfo(model string) (interface{}, interface{}) {
	pool := r.GetPool(model)
	if pool == nil {
		utils.L().Debugf("GetCredentialWithPenaltyInfo: pool is nil for model %s", model)
		return nil, nil
	}

	return pool.GetNextWithPenaltyInfo()
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
			if isRateLimitError(err) {
				resetTime := extractQuotaResetTime(err)

				if !resetTime.IsZero() {
					// Type 1 or Type 2: Explicit reset time provided
					// Set AvailableAt - credential won't be selected until this time
					cred.AvailableAt = resetTime
					cred.FailureCount = 0 // Reset failure count since we have explicit reset time
				} else {
					// Type 3: No explicit reset time - use exponential backoff
					cred.FailureCount++
					backoffDuration := getExponentialBackoffDuration(cred.FailureCount)
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
	cred, _ := p.GetNextWithPenaltyInfo()
	return cred
}

// GetNextWithPenaltyInfo returns a credential based on round-robin selection with penalty information.
// Implements the following logic:
// 1. Round-robin selection based on LastUsedAt timestamp
// 2. Skip credentials that are in rate limit penalty (AvailableAt > now)
// 3. Skip credentials that are dead
// 4. Update LastUsedAt when a credential is selected
// 5. Returns penalty info if all credentials are in penalty
func (p *CredentialPool) GetNextWithPenaltyInfo() (*auth.Credential, *InternalPenaltyInfo) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.credentials) == 0 {
		utils.L().Debugf("GetNextWithPenaltyInfo: credential pool is empty")
		return nil, nil
	}

	now := time.Now()
	var selectedCred *auth.Credential
	var oldestLastUsed time.Time
	var shortestResetTime time.Time
	var allInPenalty = true

	// Find the oldest used credential that is not in penalty
	for _, cred := range p.credentials {
		// Check if credential is in rate limit penalty
		if !cred.AvailableAt.IsZero() && now.Before(cred.AvailableAt) {
			utils.L().Debugf("GetNextWithPenaltyInfo: credential %s is in penalty until %v (AvailableAt=%v, now=%v)", cred.ID, cred.AvailableAt, cred.AvailableAt, now)
			// Track the shortest reset time among penalized credentials
			if shortestResetTime.IsZero() || cred.AvailableAt.Before(shortestResetTime) {
				shortestResetTime = cred.AvailableAt
			}
			continue
		}

		// This credential is not in penalty
		allInPenalty = false
		utils.L().Debugf("GetNextWithPenaltyInfo: credential %s is NOT in penalty, state=%s, AvailableAt=%v", cred.ID, cred.State, cred.AvailableAt)

		// Check if credential is dead
		if cred.State == auth.CredentialStateDead {
			utils.L().Debugf("GetNextWithPenaltyInfo: credential %s is dead, skipping", cred.ID)
			continue
		}

		// Track the oldest used credential for round-robin
		if selectedCred == nil || cred.LastUsedAt.Before(oldestLastUsed) {
			selectedCred = cred
			oldestLastUsed = cred.LastUsedAt
		}
	}

	// If no healthy credential found, return penalty info
	if selectedCred == nil {
		return nil, &InternalPenaltyInfo{
			AllInPenalty:      allInPenalty,
			ShortestResetTime: shortestResetTime,
		}
	}

	utils.L().Debugf("GetNextWithPenaltyInfo: selected credential %s", selectedCred.ID)

	// Update LastUsedAt for round-robin
	selectedCred.LastUsedAt = now

	// Clear AvailableAt if it has passed to keep credential object clean
	if !selectedCred.AvailableAt.IsZero() && now.After(selectedCred.AvailableAt) {
		selectedCred.AvailableAt = time.Time{}
	}

	return selectedCred, nil
}
