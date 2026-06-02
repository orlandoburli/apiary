package daemon

import (
	"math"
	"strings"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
)

// RetryManager handles retry logic and backoff calculations.
type RetryManager struct {
	policy *config.RetryPolicy
}

// NewRetryManager creates a retry manager from the config's retry policy.
func NewRetryManager(policy *config.RetryPolicy) *RetryManager {
	if policy == nil {
		policy = &config.RetryPolicy{
			Enabled:         false,
			MaxAttempts:     1,
			BackoffStrategy: "exponential",
			BackoffBase:     "1s",
		}
	}
	return &RetryManager{policy: policy}
}

// IsRetriable checks if an error should trigger a retry based on the policy.
func (rm *RetryManager) IsRetriable(errMsg string) bool {
	if !rm.policy.Enabled {
		return false
	}

	// Check non-retriable errors first (deny-list)
	for _, pattern := range rm.policy.NonRetriableErrors {
		if strings.Contains(errMsg, pattern) {
			return false
		}
	}

	// Check retriable errors (allow-list)
	if len(rm.policy.RetriableErrors) == 0 {
		// If no retriable patterns are specified, retry all errors
		return true
	}
	for _, pattern := range rm.policy.RetriableErrors {
		if strings.Contains(errMsg, pattern) {
			return true
		}
	}

	// Default: if we have a retriable list but it doesn't match, don't retry
	return len(rm.policy.RetriableErrors) == 0
}

// GetBackoffDuration calculates the wait time before retrying based on attempt number.
func (rm *RetryManager) GetBackoffDuration(attempt int) time.Duration {
	baseBackoff := rm.policy.ParsedBackoff()

	switch rm.policy.BackoffStrategy {
	case "exponential":
		// 1s, 2s, 4s, 8s, etc. (backoff_base * 2^(attempt-1))
		return baseBackoff * time.Duration(math.Pow(2, float64(attempt-1)))
	case "fixed":
		// Same backoff every time
		return baseBackoff
	default:
		// Default to exponential
		return baseBackoff * time.Duration(math.Pow(2, float64(attempt-1)))
	}
}

// ShouldRetry checks if we can retry based on attempt count.
func (rm *RetryManager) ShouldRetry(attempt int) bool {
	if !rm.policy.Enabled {
		return false
	}
	return attempt < rm.policy.MaxAttempts
}
