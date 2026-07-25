// Package redact provides token-pattern sanitisation shared across log and db.
package redact

import (
	"regexp"
	"strings"
)

var rules = []struct {
	re          *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`ghp_[A-Za-z0-9_]{10,}`), "ghp_[REDACTED]"},
	{regexp.MustCompile(`github_pat_[A-Za-z0-9_]{10,}`), "github_pat_[REDACTED]"},
	{regexp.MustCompile(`xoxb-[A-Za-z0-9\-]+`), "xoxb-[REDACTED]"},
	{regexp.MustCompile(`xoxp-[A-Za-z0-9\-]+`), "xoxp-[REDACTED]"},
	{regexp.MustCompile(`AKIA[A-Z0-9]{16}`), "AKIA[REDACTED]"},
	{regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9\-._~+/]+=*`), "bearer [REDACTED]"},
	// JWT: three base64url segments starting with eyJ
	{regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), "[REDACTED-JWT]"},
}

// String replaces recognised token patterns within s with placeholder text.
func String(s string) string {
	for _, r := range rules {
		s = r.re.ReplaceAllString(s, r.replacement)
	}
	return s
}

// LooksLikeSecret reports whether the entire string value appears to be a raw
// token or secret (used by the event-metadata key-value redactor).
func LooksLikeSecret(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"ghp_", "github_pat_", "xoxb-", "xoxp-", "bearer "} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return strings.Contains(value, "AKIA")
}
