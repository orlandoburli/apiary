package db

import (
	"testing"
	"time"
)

// The liveness rule decides what the dashboard shows as working and what the
// daemon's reaper closes out. It is defined once, in this package, precisely
// so those two can never disagree about the same execution.
func TestExecutionIsAlive(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-5 * time.Second)
	old := now.Add(-time.Hour)
	started := now.Add(-time.Minute)
	longStarted := now.Add(-time.Hour)

	cases := []struct {
		name      string
		pid       int
		heartbeat *time.Time
		started   *time.Time
		want      bool
	}{
		{"fresh heartbeat with a pid", 1234, &fresh, &started, true},
		{"fresh heartbeat without a pid", 0, &fresh, &started, true},
		{"heartbeat exactly at the window edge", 1234, ptr(now.Add(-ExecutionHeartbeatWindow)), &started, true},
		{"stale heartbeat", 1234, &old, &started, false},
		{"stale heartbeat without a pid", 0, &old, &started, false},
		{"no heartbeat, pid, young row", 1234, nil, &started, true},
		{"no heartbeat, pid, old row", 1234, nil, &longStarted, false},
		{"no heartbeat, no pid", 0, nil, &started, false},
	}
	for _, tc := range cases {
		if got := ExecutionIsAlive(tc.pid, tc.heartbeat, tc.started, now); got != tc.want {
			t.Errorf("%s: ExecutionIsAlive = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A row cannot be both alive and stale. The dashboard marks a row alive by one
// rule and the reaper closes it by the other; if a row could satisfy both, the
// reaper would close work the dashboard is showing as running.
func TestExecutionIsAliveAndStaleAreComplementary(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-5 * time.Second)
	old := now.Add(-time.Hour)
	started := now.Add(-time.Minute)
	longStarted := now.Add(-time.Hour)

	cases := []struct {
		name      string
		pid       int
		heartbeat *time.Time
		started   *time.Time
	}{
		{"fresh heartbeat with a pid", 1234, &fresh, &started},
		{"stale heartbeat", 1234, &old, &started},
		{"no heartbeat, pid, young row", 1234, nil, &started},
		{"no heartbeat, pid, old row", 1234, nil, &longStarted},
		{"no heartbeat, no pid", 0, nil, &started},
	}
	for _, tc := range cases {
		alive := ExecutionIsAlive(tc.pid, tc.heartbeat, tc.started, now)
		stale := ExecutionIsStale(tc.pid, tc.heartbeat, tc.started, now)
		if alive && stale {
			t.Errorf("%s: row is both alive and stale — the two views would disagree", tc.name)
		}
	}
}

// A row that carries no evidence of a live process is only stale once it is
// old enough that the absence cannot be a write that has not landed yet. "No
// heartbeat yet" is what a row looks like in the moments before the daemon
// writes the first one.
func TestExecutionIsStaleNeedsGraceForEvidencelessRows(t *testing.T) {
	now := time.Now()

	// A row that started a minute ago and has no evidence: not stale yet.
	justStarted := now.Add(-time.Minute)
	if ExecutionIsStale(4321, nil, &justStarted, now) {
		t.Error("a row that just started with no heartbeat must not be stale")
	}

	// The same row, long past the grace period: stale.
	longAgo := now.Add(-ExecutionHeartbeatGrace - time.Minute)
	if !ExecutionIsStale(4321, nil, &longAgo, now) {
		t.Error("a row past the grace period with no heartbeat must be stale")
	}

	// A row with no heartbeat, no pid and no start time cannot be judged, so
	// it is left alone rather than guessed at.
	if ExecutionIsStale(4321, nil, nil, now) {
		t.Error("a row with neither heartbeat nor start time must not be judged stale")
	}
}

// A stale heartbeat is stale regardless of how long the run has been going: the
// heartbeat is the signal, not the age of the run.
func TestExecutionIsStaleIgnoresRunAgeWhenHeartbeatIsPresent(t *testing.T) {
	now := time.Now()
	old := now.Add(-2 * time.Hour)
	started := now.Add(-time.Minute)

	if !ExecutionIsStale(1234, &old, &started, now) {
		t.Error("a two-hour-old heartbeat must be stale even if the run started a minute ago")
	}
}

func ptr(t time.Time) *time.Time { return &t }
