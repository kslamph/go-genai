package manager

import (
	"sync"
	"time"

	"github.com/sunbankio/omniproxy/pkg/utils"
)

// RateLimitTracker manages rate limit state for providers
type RateLimitTracker struct {
	// rateLimitRejectTime maps "providerName:model" to the timestamp when it was last rejected due to 429
	rateLimitRejectTime sync.Map // map[string]time.Time

	// quotaExhaustedResetTime maps "providerName:model" to the timestamp when quota will be reset
	quotaExhaustedResetTime sync.Map // map[string]time.Time
}

// NewRateLimitTracker creates a new RateLimitTracker
func NewRateLimitTracker() *RateLimitTracker {
	return &RateLimitTracker{}
}

// RecordRateLimitReject records a 429 rate limit rejection for a specific provider-model combination
func (rlt *RateLimitTracker) RecordRateLimitReject(providerName string, model string) {
	key := providerName + ":" + model
	now := time.Now()
	rlt.rateLimitRejectTime.Store(key, now)
	utils.L().Infow("Recorded 429 rate limit rejection",
		"provider", providerName,
		"model", model,
		"reject_time", now.Format(time.RFC3339))
}

// RecordQuotaExhausted records a 429 quota exhaustion with explicit reset time for a specific provider-model combination
func (rlt *RateLimitTracker) RecordQuotaExhausted(providerName string, model string, resetTime time.Time) {
	key := providerName + ":" + model
	rlt.quotaExhaustedResetTime.Store(key, resetTime)
	utils.L().Infow("Recorded 429 quota exhaustion with reset time",
		"provider", providerName,
		"model", model,
		"reset_time", resetTime.Format(time.RFC3339),
		"reset_in", time.Until(resetTime).String())
}

// GetLastRejectTime returns the last time a provider-model combination was rate limited
func (rlt *RateLimitTracker) GetLastRejectTime(providerName string, model string) time.Time {
	key := providerName + ":" + model
	val, ok := rlt.rateLimitRejectTime.Load(key)
	if !ok {
		return time.Time{}
	}
	return val.(time.Time)
}

// GetQuotaResetTime returns the reset time for a provider-model combination if quota is exhausted
func (rlt *RateLimitTracker) GetQuotaResetTime(providerName string, model string) (time.Time, bool) {
	key := providerName + ":" + model
	val, ok := rlt.quotaExhaustedResetTime.Load(key)
	if !ok {
		return time.Time{}, false
	}
	return val.(time.Time), true
}

// IsBlocked checks if a provider-model combination is currently blocked due to rate limiting or quota exhaustion
func (rlt *RateLimitTracker) IsBlocked(providerName string, model string) bool {
	key := providerName + ":" + model
	
	// Check if quota is exhausted
	if val, ok := rlt.quotaExhaustedResetTime.Load(key); ok {
		resetTime := val.(time.Time)
		if time.Now().Before(resetTime) {
			return true
		}
		// Quota reset time has passed, remove the entry
		rlt.quotaExhaustedResetTime.Delete(key)
	}
	
	return false
}
