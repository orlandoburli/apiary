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

	// redactPatterns matches well-known credential formats embedded in log messages.
	// Patterns mirror the value heuristics in internal/db/execution_event.go.
	redactPatterns = []*regexp.Regexp{
		// JWT: three base64url segments starting with the standard JSON header prefix
		regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`),
		// GitHub personal access tokens (classic and fine-grained)
		regexp.MustCompile(`ghp_[A-Za-z0-9_-]+`),
		regexp.MustCompile(`github_pat_[A-Za-z0-9_-]+`),
		// Slack bot/user tokens
		regexp.MustCompile(`xox[bp]-[A-Za-z0-9-]+`),
		// AWS access key IDs
		regexp.MustCompile(`AKIA[A-Z0-9]{16}`),
	}
	// bearerPattern keeps the "bearer " prefix so the log line stays readable.
	bearerPattern = regexp.MustCompile(`(?i)(bearer\s+)\S+`)
)

// redact removes well-known credential patterns from msg before it reaches
// stderr or any registered sink.
func redact(msg string) string {
	for _, p := range redactPatterns {
		msg = p.ReplaceAllString(msg, "[REDACTED]")
	}
	return bearerPattern.ReplaceAllString(msg, "${1}[REDACTED]")
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
	msg := redact(fmt.Sprintf(format, args...))
	fmt.Fprintf(os.Stderr, "%s  %-5s  %s\n", ts, level, msg)

	sinkMu.RLock()
	f := sink
	sinkMu.RUnlock()
	if f != nil {
		f(level, msg)
	}
}
