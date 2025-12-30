package manager

import (
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sunbankio/omniproxy/internal/provider"
)

// LoadBalancer handles provider selection using various strategies
type LoadBalancer struct {
	// lastSuccess maps model name to the last successful provider instance
	lastSuccess sync.Map // map[string]provider.BaseProvider

	// failureCount tracks recent failures per provider (for smart selection)
	failureCount sync.Map // map[string]*atomic.Int64

	// lastUsedIndex tracks the last used index for round-robin per provider type
	lastUsedIndex sync.Map // map[string]*atomic.Int64

	// rngMu protects rng
	rngMu sync.Mutex
	// rng for random selection (fallback only)
	rng *rand.Rand
}

// NewLoadBalancer creates a new LoadBalancer
func NewLoadBalancer() *LoadBalancer {
	return &LoadBalancer{
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (lb *LoadBalancer) getFailureCount(providerName string) *atomic.Int64 {
	val, ok := lb.failureCount.Load(providerName)
	if !ok {
		val, _ = lb.failureCount.LoadOrStore(providerName, new(atomic.Int64))
	}
	return val.(*atomic.Int64)
}

func (lb *LoadBalancer) getLastUsedIndex(poolKey string) *atomic.Int64 {
	val, ok := lb.lastUsedIndex.Load(poolKey)
	if !ok {
		val, _ = lb.lastUsedIndex.LoadOrStore(poolKey, new(atomic.Int64))
	}
	return val.(*atomic.Int64)
}

// RecordSuccess records a successful request for a model with a provider
func (lb *LoadBalancer) RecordSuccess(model string, p provider.BaseProvider) {
	lb.lastSuccess.Store(model, p)
	lb.getFailureCount(p.Name()).Store(0)
}

// RecordFailure records a failed request for a provider
func (lb *LoadBalancer) RecordFailure(p provider.BaseProvider) {
	lb.getFailureCount(p.Name()).Add(1)
}

// GetLastSuccess returns the last successful provider for a model, if any
func (lb *LoadBalancer) GetLastSuccess(model string) provider.BaseProvider {
	val, ok := lb.lastSuccess.Load(model)
	if !ok {
		return nil
	}
	return val.(provider.BaseProvider)
}

// SelectRoundRobin selects a provider from a pool using round-robin with failure tracking
func (lb *LoadBalancer) SelectRoundRobin(pool []provider.BaseProvider, poolKey provider.ProviderType) provider.BaseProvider {
	if len(pool) == 0 {
		return nil
	}

	if len(pool) == 1 {
		return pool[0]
	}

	// Find the provider with the minimum failure count
	minFailures := int64(-1)
	var candidates []int

	for i, p := range pool {
		failures := lb.getFailureCount(p.Name()).Load()
		if minFailures == -1 || failures < minFailures {
			minFailures = failures
			candidates = []int{i}
		} else if failures == minFailures {
			candidates = append(candidates, i)
		}
	}

	// If multiple candidates with same failure count, use round-robin among them
	var selectedIdx int
	if len(candidates) == 1 {
		selectedIdx = candidates[0]
	} else {
		lastUsed := lb.getLastUsedIndex(string(poolKey))
		lastIdx := int(lastUsed.Load())

		found := false
		for _, idx := range candidates {
			if idx > lastIdx {
				selectedIdx = idx
				found = true
				break
			}
		}

		if !found {
			selectedIdx = candidates[0]
		}
		lastUsed.Store(int64(selectedIdx))
	}

	return pool[selectedIdx]
}

// SelectLeastRecentlyRejected selects a provider from a pool preferring providers with fewer rejections
func (lb *LoadBalancer) SelectLeastRecentlyRejected(pool []provider.BaseProvider, poolKey string, rateLimitTracker *RateLimitTracker) provider.BaseProvider {
	if len(pool) == 0 {
		return nil
	}

	if len(pool) == 1 {
		// Check if the single provider is blocked
		if rateLimitTracker.IsBlocked(pool[0].Name(), poolKey) {
			return nil
		}
		return pool[0]
	}

	// Filter out blocked providers first
	type candidate struct {
		provider   provider.BaseProvider
		lastReject time.Time
	}

	var candidates []candidate
	for _, p := range pool {
		// Skip providers that are currently blocked (quota exhausted)
		if rateLimitTracker.IsBlocked(p.Name(), poolKey) {
			continue
		}
		lastReject := rateLimitTracker.GetLastRejectTime(p.Name(), poolKey)
		candidates = append(candidates, candidate{
			provider:   p,
			lastReject: lastReject,
		})
	}

	// If all providers are blocked, return nil
	if len(candidates) == 0 {
		return nil
	}

	// Select the provider with the oldest rejection time (or zero if never rejected)
	selected := candidates[0].provider
	oldestReject := candidates[0].lastReject

	for _, c := range candidates[1:] {
		if c.lastReject.Before(oldestReject) {
			oldestReject = c.lastReject
			selected = c.provider
		} else if c.lastReject.Equal(oldestReject) {
			// If multiple providers have the same rejection time, randomly select one
			lb.rngMu.Lock()
			if lb.rng.Intn(2) == 0 {
				selected = c.provider
			}
			lb.rngMu.Unlock()
		}
	}

	return selected
}
