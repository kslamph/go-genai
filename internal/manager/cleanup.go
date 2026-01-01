package manager

import (
	"context"
	"time"

	"github.com/sunbankio/omniproxy/pkg/utils"
)

// DefaultCleanupInterval is the default interval for running cleanup routines
const DefaultCleanupInterval = time.Hour

// StartCleanupRoutine starts a goroutine that periodically cleans up client pools
// for all credentials in the V2 registry.
//
// The routine runs at the specified interval (or DefaultCleanupInterval if not provided)
// and calls Cleanup() on each credential's client pool to remove old clients.
func StartCleanupRoutine(ctx context.Context, registry *Registry, interval ...time.Duration) {
	// Determine the cleanup interval
	cleanupInterval := DefaultCleanupInterval
	if len(interval) > 0 && interval[0] > 0 {
		cleanupInterval = interval[0]
	}

	utils.L().Infof("Starting cleanup routine with interval: %v", cleanupInterval)

	// Create a ticker for the cleanup routine
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()

	// Run cleanup immediately at start
	runCleanup(registry)

	for {
		select {
		case <-ctx.Done():
			utils.L().Infof("Cleanup routine stopping due to context cancellation")
			return
		case <-ticker.C:
			runCleanup(registry)
		}
	}
}

// runCleanup performs the actual cleanup of all credential client pools
func runCleanup(registry *Registry) {
	utils.L().Infof("Running cleanup routine for all credential client pools")

	// Get all model pools from the registry
	registry.mu.RLock()
	modelPools := make(map[string]*CredentialPool)
	for model, pool := range registry.modelPools {
		modelPools[model] = pool
	}
	registry.mu.RUnlock()

	totalCleaned := 0

	// Iterate through all model pools
	for model, pool := range modelPools {
		// Get all credentials from the pool
		credentials := pool.List()

		// Iterate through all credentials in the pool
		for _, cred := range credentials {
			// Call Cleanup() on the credential to clean up its client pool
			cred.Cleanup()
			totalCleaned++
		}

		utils.L().Debugf("Cleaned up %d credentials for model: %s", len(credentials), model)
	}

	utils.L().Infof("Cleanup completed: cleaned up %d credentials across %d models", totalCleaned, len(modelPools))
}
