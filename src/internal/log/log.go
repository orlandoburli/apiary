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

// Patterns that identify secrets which must never appear in log output.
var (
	reJWT    = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)
	reGHPAT  = regexp.MustCompile(`ghp_[A-Za-z0-9]{10,}`)
	reGHFine = regexp.MustCompile(`github_pat_[A-Za-z0-9_]{10,}`)
	reSlack  = regexp.MustCompile(`xox[bp]-[A-Za-z0-9\-]{10,}`)
	reAWS    = regexp.MustCompile(`AKIA[A-Z0-9]{16}`)
	reBearer = regexp.MustCompile(`(?i)(bearer\s+)\S{8,}`)
)

// redact replaces known secret patterns in s with [REDACTED].
func redact(s string) string {
	s = reJWT.ReplaceAllString(s, "[REDACTED]")
	s = reGHPAT.ReplaceAllString(s, "[REDACTED]")
	s = reGHFine.ReplaceAllString(s, "[REDACTED]")
	s = reSlack.ReplaceAllString(s, "[REDACTED]")
	s = reAWS.ReplaceAllString(s, "[REDACTED]")
	s = reBearer.ReplaceAllString(s, "${1}[REDACTED]")
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
	msg := redact(fmt.Sprintf(format, args...))
	fmt.Fprintf(os.Stderr, "%s  %-5s  %s\n", ts, level, msg)

	sinkMu.RLock()
	f := sink
	sinkMu.RUnlock()
	if f != nil {
		f(level, msg)
	}
}
