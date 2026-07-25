// Package log provides a simple leveled logger for Apiary.
// Verbose mode is toggled once at startup via Enable(true).
package log

import (
	"fmt"
	"os"
	"regexp"
	"sync"
	"time"
)

var (
	verbose bool
	sinkMu  sync.RWMutex
	sink    func(level, msg string)
)

// jwtPattern matches JWT-shaped bearer tokens (three base64url segments
// starting with "eyJ"). Keeping them out of stderr prevents credential leaks
// into log files and process-inspection tools.
var jwtPattern = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)

// secretPattern matches well-known token prefixes (GitHub PATs, Slack tokens,
// AWS access-key IDs) followed by at least 8 word characters.
var secretPattern = regexp.MustCompile(`(ghp_|github_pat_|xoxb-|xoxp-|AKIA)[A-Za-z0-9_-]{8,}`)

// bearerPattern matches HTTP Authorization header values written into logs.
var bearerPattern = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9_\-.]{8,}`)

// redactSecrets replaces recognisable token patterns with placeholder text so
// they never appear on stderr or reach external log sinks.
func redactSecrets(s string) string {
	s = jwtPattern.ReplaceAllString(s, "«redacted-jwt»")
	s = secretPattern.ReplaceAllString(s, "«redacted»")
	s = bearerPattern.ReplaceAllString(s, "bearer «redacted»")
	return s
}

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
	msg := redactSecrets(fmt.Sprintf(format, args...))
	fmt.Fprintf(os.Stderr, "%s  %-5s  %s\n", ts, level, msg)

	sinkMu.RLock()
	f := sink
	sinkMu.RUnlock()
	if f != nil {
		f(level, msg)
	}
}
