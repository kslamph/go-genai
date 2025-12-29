package manager

import (
	"sync"
	"time"

	"github.com/sunbankio/omniproxy/pkg/utils"
)

// RateLimitTracker manages rate limit state for providers
type RateLimitTracker struct {
	// rateLimitRejectTime maps "providerName:model" to the timestamp when it was last rejected due to 429
	rateLimitRejectTime map[string]time.Time

	// quotaExhaustedResetTime maps "providerName:model" to the timestamp when quota will be reset
	quotaExhaustedResetTime map[string]time.Time

	// mu protects rateLimitRejectTime and quotaExhaustedResetTime
	mu sync.RWMutex
}

// NewRateLimitTracker creates a new RateLimitTracker
func NewRateLimitTracker() *RateLimitTracker {
	return &RateLimitTracker{
		rateLimitRejectTime:     make(map[string]time.Time),
		quotaExhaustedResetTime: make(map[string]time.Time),
	}
}

// RecordRateLimitReject records a 429 rate limit rejection for a specific provider-model combination
func (rlt *RateLimitTracker) RecordRateLimitReject(providerName string, model string) {
	rlt.mu.Lock()
	defer rlt.mu.Unlock()
	key := providerName + ":" + model
	rlt.rateLimitRejectTime[key] = time.Now()
	utils.L().Infow("Recorded 429 rate limit rejection",
		"provider", providerName,
		"model", model,
		"reject_time", rlt.rateLimitRejectTime[key].Format(time.RFC3339))
}

// RecordQuotaExhausted records a 429 quota exhaustion with explicit reset time for a specific provider-model combination
func (rlt *RateLimitTracker) RecordQuotaExhausted(providerName string, model string, resetTime time.Time) {
	rlt.mu.Lock()
	defer rlt.mu.Unlock()
	key := providerName + ":" + model
	rlt.quotaExhaustedResetTime[key] = resetTime
	utils.L().Infow("Recorded 429 quota exhaustion with reset time",
		"provider", providerName,
		"model", model,
		"reset_time", resetTime.Format(time.RFC3339),
		"reset_in", time.Until(resetTime).String())
}

// GetLastRejectTime returns the last time a provider-model combination was rate limited
func (rlt *RateLimitTracker) GetLastRejectTime(providerName string, model string) time.Time {
	rlt.mu.RLock()
	defer rlt.mu.RUnlock()
	key := providerName + ":" + model
	return rlt.rateLimitRejectTime[key]
}

// GetQuotaResetTime returns the reset time for a provider-model combination if quota is exhausted
func (rlt *RateLimitTracker) GetQuotaResetTime(providerName string, model string) (time.Time, bool) {
	rlt.mu.RLock()
	defer rlt.mu.RUnlock()
	key := providerName + ":" + model
	resetTime, exists := rlt.quotaExhaustedResetTime[key]
	return resetTime, exists
}

// IsBlocked checks if a provider-model combination is currently blocked due to rate limiting or quota exhaustion
func (rlt *RateLimitTracker) IsBlocked(providerName string, model string) bool {
	rlt.mu.RLock()
	defer rlt.mu.RUnlock()
	
	key := providerName + ":" + model
	
	// Check if quota is exhausted
	if resetTime, exists := rlt.quotaExhaustedResetTime[key]; exists {
		if time.Now().Before(resetTime) {
			return true
		}
		// Quota reset time has passed, remove the entry
		// Note: This requires write lock, so we'll handle this cleanup elsewhere
	}
	
	return false
}