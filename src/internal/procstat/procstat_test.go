package procstat

import (
	"testing"
	"time"
)

// ps reports CPU time as [[dd-]hh:]mm:ss[.frac], with the fraction in
// hundredths of a second. Parsing it wrongly is silent — a wrong duration
// becomes a wrong CPU percentage — so the formats are pinned here.
func TestParsePSDuration(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"0:00.50", 500 * time.Millisecond},
		{"0:01.28", 1280 * time.Millisecond},
		{"0:15.66", 15*time.Second + 660*time.Millisecond},
		{"1:02.03", 62*time.Second + 30*time.Millisecond},
		{"2:00:00.00", 2 * time.Hour},
		{"1-00:00:00.00", 24 * time.Hour},
		{"0:00.04", 40 * time.Millisecond},
	}
	for _, tc := range cases {
		got, err := parsePSDuration(tc.in)
		if err != nil {
			t.Errorf("parsePSDuration(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parsePSDuration(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// A malformed value must be an error, not a zero: a process whose CPU time
// cannot be read should not silently contribute nothing to the total.
func TestParsePSDurationRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "abc", "1:x:00", "1:2:3:4"} {
		if _, err := parsePSDuration(in); err == nil {
			t.Errorf("parsePSDuration(%q) should have failed", in)
		}
	}
}

// The tree walk must include the root's whole subtree — an agent CLI and the
// MCP servers and node children it spawned — and nothing else.
func TestDescendantsWalksTheWholeSubtree(t *testing.T) {
	usage := &Usage{Processes: []Process{
		{PID: 1, PPID: 0},     // init
		{PID: 100, PPID: 1},   // the daemon
		{PID: 200, PPID: 100}, // an agent CLI
		{PID: 201, PPID: 200}, // its node child
		{PID: 202, PPID: 201}, // a grandchild
		{PID: 300, PPID: 100}, // a second agent CLI
		{PID: 400, PPID: 1},   // somebody else's process
	}}
	got := usage.Descendants(100)
	want := map[int]bool{200: true, 201: true, 202: true, 300: true}
	if len(got) != len(want) {
		t.Fatalf("Descendants(100) = %+v, want %d processes", got, len(want))
	}
	for _, p := range got {
		if !want[p.PID] {
			t.Errorf("Descendants(100) included %d, which is not under 100", p.PID)
		}
	}
}

// Tree sums the root and its whole subtree. The root must be counted: the
// daemon's own memory is part of what the daemon costs.
func TestTreeIncludesTheRootAndItsSubtree(t *testing.T) {
	usage := &Usage{Processes: []Process{
		{PID: 100, PPID: 1, RSS: 100, CPUPercent: 1},
		{PID: 200, PPID: 100, RSS: 20, CPUPercent: 2},
		{PID: 300, PPID: 200, RSS: 3, CPUPercent: 3},
		{PID: 400, PPID: 1, RSS: 4000, CPUPercent: 40},
	}}
	got := usage.Tree(100)
	if got.Processes != 3 {
		t.Errorf("Processes = %d, want 3 (the root and two descendants)", got.Processes)
	}
	if got.RSS != 123 {
		t.Errorf("RSS = %d, want 123", got.RSS)
	}
	if got.CPUPercent != 6 {
		t.Errorf("CPUPercent = %v, want 6", got.CPUPercent)
	}
}

// A nil snapshot or a missing PID must not panic: the dashboard renders before
// its first sample lands.
func TestTreeToleratesNoData(t *testing.T) {
	var u *Usage
	if got := u.Tree(1); got.Processes != 0 {
		t.Errorf("Tree on a nil snapshot = %+v, want zero", got)
	}
	if got := (&Usage{}).Tree(0); got.Processes != 0 {
		t.Errorf("Tree(0) = %+v, want zero", got)
	}
	if got := (&Usage{}).Tree(999); got.Processes != 0 {
		t.Errorf("Tree of a missing pid = %+v, want zero", got)
	}
}

// A process whose parent is itself (or a cycle) must not send the walk into
// an infinite loop.
func TestDescendantsTerminatesOnCycles(t *testing.T) {
	usage := &Usage{Processes: []Process{
		{PID: 10, PPID: 20},
		{PID: 20, PPID: 10},
	}}
	done := make(chan struct{})
	go func() {
		usage.Descendants(10)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Descendants did not terminate on a parent cycle")
	}
}

// The byte formatting is what an operator reads, so the units are pinned.
func TestFormatBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{512, "512 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1024 * 1024, "1.0 MiB"},
		{87 * 1024 * 1024, "87.0 MiB"},
	}
	for _, tc := range cases {
		if got := FormatBytes(tc.in); got != tc.want {
			t.Errorf("FormatBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The sampler must report an instantaneous CPU figure, not the lifetime
// average ps reports on its own.
//
// The distinction matters most for short-lived agents: a process that burned
// 2 seconds of CPU over its 10-second life reads as 20% under a lifetime
// average and as 100% under a delta — and the second is what "is this pegging
// my CPU" means.
func TestSamplerReportsInstantaneousCPU(t *testing.T) {
	s := NewSampler()
	first, err := s.Read()
	if err != nil {
		t.Skipf("cannot read process table here: %v", err)
	}
	if first.CPUPercent != 0 {
		t.Errorf("the first sample reports %v%%, want 0 — there is nothing to difference against yet", first.CPUPercent)
	}
	if !first.Baseline {
		t.Error("the first sample is not marked as a baseline")
	}
	if len(first.Processes) == 0 {
		t.Fatal("the process table read nothing")
	}

	// The test process itself is burning CPU while we sleep, so the second
	// sample must report more than the first.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_ = make([]byte, 1<<20)
	}
	second, err := s.Read()
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if second.CPUPercent < 0 {
		t.Errorf("CPU percent = %v, want a non-negative figure", second.CPUPercent)
	}
	if second.Baseline {
		t.Error("the second sample is still marked as a baseline")
	}
	if second.TotalRSS <= 0 {
		t.Errorf("total RSS = %d, want a positive figure", second.TotalRSS)
	}
	if second.SampledAt.IsZero() {
		t.Error("the snapshot has no timestamp")
	}
}
