package execution

import (
	"strings"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
)

// FailureDetector inspects a completed run and classifies why it failed to
// produce useful work. Each runner type registers its own detector; the generic
// detector serves as the default for any runner without a specific one.
type FailureDetector interface {
	// Detect returns the failure kind and an optional reset time.
	// Called after Run() returns, with access to the full result.
	Detect(req model.RunRequest, result *model.RunResult) (kind model.FailureKind, resetsAt time.Time)
}

// detectorRegistry maps adapter type key to its failure detector.
var detectorRegistry = map[string]FailureDetector{}

// RegisterFailureDetector registers a detector for the given runner adapter type.
func RegisterFailureDetector(adapterType string, d FailureDetector) {
	detectorRegistry[adapterType] = d
}

// FailureDetectorFor returns the registered detector for the given adapter type,
// or the genericDetector if none is registered.
func FailureDetectorFor(adapterType string) FailureDetector {
	if d, ok := detectorRegistry[adapterType]; ok {
		return d
	}
	return genericDetector{}
}

// genericDetector is the default failure detector. It classifies failures based
// on exit code, output content, and known error patterns.
type genericDetector struct{}

func (genericDetector) Detect(_ model.RunRequest, result *model.RunResult) (model.FailureKind, time.Time) {
	// If the runner itself already flagged it, trust that.
	if result.RateLimited {
		return model.FailureRateLimited, result.RateLimitResetsAt
	}

	if result.Success {
		return model.FailureNone, time.Time{}
	}

	// If there's a non-nil error, check for credit/billing signals.
	if result.Error != nil {
		errStr := result.Error.Error()
		outputStr := result.Output

		combined := strings.ToLower(errStr + " " + outputStr)

		// Credit exhaustion patterns (provider-agnostic).
		creditPatterns := []string{
			"out of credits",
			"credit limit",
			"insufficient credits",
			"credits exhausted",
			"insufficient_quota",
			"exceeded your usage",
			"billing limit",
			"payment required",
			"account balance",
			"credit_exhausted",
		}
		for _, p := range creditPatterns {
			if strings.Contains(combined, p) {
				return model.FailureCreditExhausted, time.Now().Add(24 * time.Hour)
			}
		}

		// Rate-limit patterns (provider error messages, not JSON events).
		rateLimitPatterns := []string{
			"rate limit",
			"rate_limit",
			"too many requests",
			"429",
		}
		for _, p := range rateLimitPatterns {
			if strings.Contains(combined, p) {
				return model.FailureRateLimited, time.Now().Add(5 * time.Minute)
			}
		}
	}

	// Non-zero exit with empty or error-only output: aborted.
	output := strings.TrimSpace(result.Output)
	if output == "" || isErrorOnlyOutput(output) {
		return model.FailureAborted, time.Time{}
	}

	return model.FailureNone, time.Time{}
}

// isErrorOnlyOutput returns true when the output consists only of known
// error/limit messages with no substantive content.
func isErrorOnlyOutput(s string) bool {
	s = strings.ToLower(s)
	lines := strings.Split(s, "\n")
	errorish := 0
	total := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		total++
		if strings.Contains(line, "error") ||
			strings.Contains(line, "failed") ||
			strings.Contains(line, "limit") ||
			strings.Contains(line, "unavailable") ||
			strings.Contains(line, "timeout") {
			errorish++
		}
	}
	return total > 0 && errorish == total
}
