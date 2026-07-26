// Package log provides a simple leveled logger for Apiary.
// Verbose mode is toggled once at startup via Enable(true).
package log

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	verbose bool
	sinkMu  sync.RWMutex
	sink    func(level, msg string)
)

// Enable turns verbose (debug) output on or off.
func Enable(v bool) { verbose = v }

// Verbose returns true if verbose mode is active.
func Verbose() bool { return verbose }

// SetSink registers a callback that receives every printed message (the same
// ones that go to stderr), so operational logs can also be persisted — e.g. to
// the service-log table the dashboard's Logs tab reads. Pass nil to detach.
func SetSink(f func(level, msg string)) {
	sinkMu.Lock()
	sink = f
	sinkMu.Unlock()
}

// Info always prints to stderr.
func Info(format string, args ...any) {
	print("INFO", format, args...)
}

// Debug prints only when verbose mode is enabled.
func Debug(format string, args ...any) {
	if !verbose {
		return
	}
	print("DEBUG", format, args...)
}

// Warn always prints to stderr.
func Warn(format string, args ...any) {
	print("WARN", format, args...)
}

// Error always prints to stderr.
func Error(format string, args ...any) {
	print("ERROR", format, args...)
}

func print(level, format string, args ...any) {
	ts := time.Now().Format("15:04:05")
	msg := redactMessage(fmt.Sprintf(format, args...))
	fmt.Fprintf(os.Stderr, "%s  %-5s  %s\n", ts, level, msg)

	sinkMu.RLock()
	f := sink
	sinkMu.RUnlock()
	if f != nil {
		f(level, msg)
	}
}

// jwtPattern matches JWT-shaped bearer tokens (three base64url segments
// starting with "eyJ"). Mirrors the pattern used in transcript.go.
var jwtPattern = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)

// tokenPrefixPattern matches known secret prefixes followed by non-whitespace
// characters — GitHub PATs, Slack tokens, AWS access keys, and Bearer values.
var tokenPrefixPattern = regexp.MustCompile(
	`(?i)(ghp_|github_pat_|xoxb-|xoxp-|bearer )\S+|AKIA[A-Z0-9]{16}`,
)

// redactMessage scrubs known secret patterns from a freeform log message
// before it reaches stderr or the sink.
func redactMessage(msg string) string {
	msg = jwtPattern.ReplaceAllString(msg, "«redacted-jwt»")
	msg = tokenPrefixPattern.ReplaceAllStringFunc(msg, func(match string) string {
		lower := strings.ToLower(match)
		for _, prefix := range []string{"ghp_", "github_pat_", "xoxb-", "xoxp-", "bearer "} {
			if strings.HasPrefix(lower, prefix) {
				return match[:len(prefix)] + "[REDACTED]"
			}
		}
		return "[REDACTED]" // AWS AKIA key
	})
	return msg
}
