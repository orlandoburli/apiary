package config

import (
	"testing"
	"time"
)

// The reported bug (#411) was a documentation mismatch, not a broken mechanism:
// the docs said the default was 30m in three places while the code used 2h, so
// steps running 66-84 minutes were never cut off and the operator had no way to
// know why. Pinning the real value here means the docs and the code can only
// drift again through a deliberate edit.
func TestTaskTimeoutDefault(t *testing.T) {
	var s Settings
	if got := s.TaskTimeoutDuration(); got != DefaultTaskTimeout {
		t.Errorf("unset task_timeout = %v, want %v", got, DefaultTaskTimeout)
	}
	if DefaultTaskTimeout != 2*time.Hour {
		t.Errorf("DefaultTaskTimeout = %v; docs/configuration.md, docs/resilience.md "+
			"and schema/apiary.json all state this value and must be updated together",
			DefaultTaskTimeout)
	}
}

func TestTaskTimeoutParsing(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", DefaultTaskTimeout},
		{"30m", 30 * time.Minute},
		{"2h", 2 * time.Hour},
		{"90s", 90 * time.Second},
		{"nonsense", DefaultTaskTimeout}, // unparseable falls back rather than disabling the bound
	}
	for _, tc := range cases {
		s := Settings{TaskTimeout: tc.in}
		if got := s.TaskTimeoutDuration(); got != tc.want {
			t.Errorf("task_timeout %q = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestStallTimeoutDefaultsOff(t *testing.T) {
	var s Settings
	if got := s.StallTimeoutDuration(); got != 0 {
		t.Errorf("unset stall_timeout = %v, want 0 (disabled)", got)
	}
}

func TestStallTimeoutParsing(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"0", 0},
		{"20m", 20 * time.Minute},
		{"-5m", 0},      // negative is meaningless; treat as disabled
		{"nonsense", 0}, // unparseable must not silently enable a killer
	}
	for _, tc := range cases {
		s := Settings{StallTimeout: tc.in}
		if got := s.StallTimeoutDuration(); got != tc.want {
			t.Errorf("stall_timeout %q = %v, want %v", tc.in, got, tc.want)
		}
	}
}
