package router

import (
	"strings"
	"time"
)

// ExtractQuotaResetTime extracts the quota reset time from a 429 error message
// Returns the reset time if the error is QUOTA_EXHAUSTED, otherwise returns zero time
// Handles three types of 429 errors:
// Type 1: "Your quota will reset after {duration}" (e.g., "4h53m59s", "49s")
// Type 2: "retryDelay:{seconds}s" or "quotaResetDelay:{duration}s"
// Type 3: 429 without explicit reset time (returns zero time)
func ExtractQuotaResetTime(err error) time.Time {
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
		if strings.Contains(errStr, pattern) {
			parts := strings.Split(errStr, pattern)
			if len(parts) > 1 {
				delayStr := strings.TrimSpace(parts[1])
				// Extract the duration (it should end with 's' or have a delimiter)
				delimiters := []string{"s", " ", ",", "]", "}"}
				for _, delim := range delimiters {
					if idx := strings.Index(delayStr, delim); idx > 0 {
						delayStr = delayStr[:idx] + "s" // Ensure it ends with 's' for ParseDuration
						break
					}
				}
				if duration, err := time.ParseDuration(delayStr); err == nil {
					return time.Now().Add(duration)
				}
			}
		}
	}

	// Type 3: No explicit reset time - return zero time
	return time.Time{}
}

// IsRateLimitError checks if an error is a 429 rate limit error
func IsRateLimitError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	return strings.Contains(errStr, "429") ||
		strings.Contains(errStr, "QUOTA_EXHAUSTED") ||
		strings.Contains(errStr, "RATE_LIMIT_EXCEEDED")
}

// GetExponentialBackoffDuration calculates the exponential backoff duration based on failure count
// Backoff sequence: 30s, 1m, 3m, 5m, 10m, 30m, 60m, 60m, ...
func GetExponentialBackoffDuration(failureCount int) time.Duration {
	backoffs := []time.Duration{
		30 * time.Second, // 30s
		1 * time.Minute,  // 1m
		3 * time.Minute,  // 3m
		5 * time.Minute,  // 5m
		10 * time.Minute, // 10m
		30 * time.Minute, // 30m
		60 * time.Minute, // 60m
	}

	if failureCount <= 0 {
		return backoffs[0]
	}

	if failureCount <= len(backoffs) {
		return backoffs[failureCount-1]
	}

	// Cap at 60 minutes for high failure counts
	return backoffs[len(backoffs)-1]
}
