package manager

import (
	"sync"
	"sync/atomic"

	"github.com/sunbankio/omniproxy/internal/provider"
)

// LoadBalancer handles provider selection using round-robin with failure tracking
type LoadBalancer struct {
	// failureCount tracks recent failures per provider (for smart selection)
	failureCount sync.Map // map[string]*atomic.Int64

	// lastUsedIndex tracks the last used index for round-robin per provider type
	lastUsedIndex sync.Map // map[string]*atomic.Int64
}

// NewLoadBalancer creates a new LoadBalancer
func NewLoadBalancer() *LoadBalancer {
	return &LoadBalancer{}
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
	lb.getFailureCount(p.Name()).Store(0)
}

// RecordFailure records a failed request for a provider
func (lb *LoadBalancer) RecordFailure(p provider.BaseProvider) {
	lb.getFailureCount(p.Name()).Add(1)
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
