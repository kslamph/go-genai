package manager

import (
	"context"
	"log"
	"time"
)

// DefaultCleanupInterval is the default interval for running cleanup routines
const DefaultCleanupInterval = time.Hour

// StartCleanupRoutine starts a goroutine that periodically cleans up client pools
// for all credentials in the provider registry.
//
// The routine runs at the specified interval (or DefaultCleanupInterval if not provided)
// and calls Cleanup() on each credential's client pool to remove old clients.
func StartCleanupRoutine(ctx context.Context, registry *ProviderRegistry, interval ...time.Duration) {
	// Determine the cleanup interval
	cleanupInterval := DefaultCleanupInterval
	if len(interval) > 0 && interval[0] > 0 {
		cleanupInterval = interval[0]
	}

	log.Printf("Starting cleanup routine with interval: %v", cleanupInterval)

	// Create a ticker for the cleanup routine
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()

	// Run cleanup immediately at start
	runCleanup(registry)

	for {
		select {
		case <-ctx.Done():
			log.Printf("Cleanup routine stopping due to context cancellation")
			return
		case <-ticker.C:
			runCleanup(registry)
		}
	}
}

// runCleanup performs the actual cleanup of all credential client pools
func runCleanup(registry *ProviderRegistry) {
	log.Printf("Running cleanup routine for all credential client pools")

	// Get all pools from the registry
	pools := registry.GetPools()
	totalCleaned := 0

	// Iterate through all pools
	for _, pool := range pools {
		// Get all credentials from the pool
		credentials := pool.List()

		// Iterate through all credentials in the pool
		for _, cred := range credentials {
			// Call Cleanup() on the credential to clean up its client pool
			cred.Cleanup()
			totalCleaned++
		}
	}

	log.Printf("Cleanup completed: cleaned up %d credentials", totalCleaned)
}
