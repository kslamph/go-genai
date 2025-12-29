package manager

import (
	"math/rand"
	"sync"
	"time"

	"github.com/sunbankio/omniproxy/internal/provider"
)

// LoadBalancer handles provider selection using various strategies
type LoadBalancer struct {
	// lastSuccess maps model name to the last successful provider instance
	lastSuccess map[string]provider.BaseProvider

	// failureCount tracks recent failures per provider (for smart selection)
	failureCount map[string]int

	// lastUsedIndex tracks the last used index for round-robin per provider type
	lastUsedIndex map[string]int

	// mu protects lastSuccess, failureCount, and lastUsedIndex
	mu sync.RWMutex

	// rng for random selection (fallback only)
	rng *rand.Rand
}

// NewLoadBalancer creates a new LoadBalancer
func NewLoadBalancer() *LoadBalancer {
	return &LoadBalancer{
		lastSuccess:   make(map[string]provider.BaseProvider),
		failureCount:  make(map[string]int),
		lastUsedIndex: make(map[string]int),
		rng:           rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// RecordSuccess records a successful request for a model with a provider
func (lb *LoadBalancer) RecordSuccess(model string, p provider.BaseProvider) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	lb.lastSuccess[model] = p
	lb.failureCount[p.Name()] = 0
}

// RecordFailure records a failed request for a provider
func (lb *LoadBalancer) RecordFailure(p provider.BaseProvider) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	lb.failureCount[p.Name()]++
}

// GetLastSuccess returns the last successful provider for a model, if any
func (lb *LoadBalancer) GetLastSuccess(model string) provider.BaseProvider {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	return lb.lastSuccess[model]
}

// SelectOpenAIProvider selects an OpenAI-compatible provider from a pool using round-robin with failure tracking
func (lb *LoadBalancer) SelectOpenAIProvider(pool []provider.OpenAICompatibleProvider, poolKey string) provider.OpenAICompatibleProvider {
	if len(pool) == 0 {
		return nil
	}
	
	if len(pool) == 1 {
		return pool[0]
	}

	lb.mu.Lock()
	defer lb.mu.Unlock()

	// Find the provider with the minimum failure count
	minFailures := -1
	var candidates []int

	for i, p := range pool {
		failures := lb.failureCount[p.Name()]
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
		lastIdx := lb.lastUsedIndex[poolKey]

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
	}

	lb.lastUsedIndex[poolKey] = selectedIdx
	return pool[selectedIdx]
}

// SelectGeminiProvider selects a Gemini provider from a pool preferring providers with fewer rejections
func (lb *LoadBalancer) SelectGeminiProvider(pool []provider.GeminiNativeProvider, poolKey string, rateLimitTracker *RateLimitTracker) provider.GeminiNativeProvider {
	if len(pool) == 0 {
		return nil
	}
	
	if len(pool) == 1 {
		return pool[0]
	}

	// Build list of candidates with their last rejection time
	type candidate struct {
		provider      provider.GeminiNativeProvider
		lastReject    time.Time
	}
	
	var candidates []candidate
	for _, p := range pool {
		lastReject := rateLimitTracker.GetLastRejectTime(p.Name(), poolKey)
		candidates = append(candidates, candidate{
			provider:   p,
			lastReject: lastReject,
		})
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
			if lb.rng.Intn(2) == 0 {
				selected = c.provider
			}
		}
	}

	return selected
}