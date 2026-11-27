package dashboard

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/db"
)

// overviewTestDurations is the shape the tab is built around: wall far above
// active (most of a run is waiting), and a shorter agent-run distribution
// underneath (a run is one process, however many steps the task had).
func overviewTestDurations() *db.ActivityDurations {
	return &db.ActivityDurations{
		Instances: db.DurationStats{
			N: 114, P50: 4*time.Hour + 24*time.Minute,
			P90: 22*time.Hour + 30*time.Minute, Max: 47*time.Hour + 48*time.Minute,
		},
		Active: db.DurationStats{
			N: 114, P50: 51 * time.Minute,
			P90: 2*time.Hour + 36*time.Minute, Max: 13 * time.Hour,
		},
		Runs: db.DurationStats{
			N: 1306, P50: 9*time.Minute + 48*time.Second,
			P90: 30 * time.Minute, Max: 2 * time.Hour,
		},
	}
}

// The Overview leads with how long work takes, not how much of it there was:
// wall and active side by side, then the agent-run distribution. Counts appear
// only as sample sizes.
func TestOverviewLeadsWithDurationDistributions(t *testing.T) {
	a := newTestApp(140, 60)
	a.model.overviewTab = &OverviewTab{
		Status:      "Healthy",
		TasksDone:   105,
		TasksFailed: 9,
		Durations:   overviewTestDurations(),
	}

	out := stripANSI(a.renderOverviewTab(60))
	for _, want := range []string{
		"Finished (last 7 days):  114 runs",
		"105 tasks done",
		"9 failed",
		"Durations (last 7 days):",
		"Wall:    p50 4.4h · p90 22.5h · max 47.8h",
		"Active:  p50 51m · p90 2.6h · max 13h",
		"Agents:  p50 9.8m · p90 30m",
		"(1306 runs)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Overview is missing %q:\n%s", want, out)
		}
	}

	// Wall and active are one comparison, and the agent runs are the context
	// underneath it: a different order would break the reading.
	wall, active := strings.Index(out, "Wall:"), strings.Index(out, "Active:")
	agents := strings.Index(out, "Agents:  p50")
	if wall < 0 || active < 0 || agents < 0 || !(wall < active && active < agents) {
		t.Errorf("want Wall, then Active, then Agents; got %d/%d/%d:\n%s", wall, active, agents, out)
	}

	// The live to-dos are kept, and are not presented as windowed figures.
	for _, want := range []string{"Running:", "Queued:", "Approvals:", "Usage (last 7 days):"} {
		if !strings.Contains(out, want) {
			t.Errorf("Overview is missing %q:\n%s", want, out)
		}
	}
}

// The count-based framing is gone from the tab, not merely de-emphasised: rates,
// success rates and averages are exactly what the operator asked to stop
// seeing.
func TestOverviewDropsCountBasedMetrics(t *testing.T) {
	a := newTestApp(140, 60)
	a.model.overviewTab = &OverviewTab{
		Status: "Healthy", TasksDone: 53, TasksFailed: 4, Durations: overviewTestDurations(),
	}

	lower := strings.ToLower(stripANSI(a.renderOverviewTab(60)))
	for _, banned := range []string{
		"throughput", "success rate", "tasks/day", "runs/day",
		"avg duration", "avg run", "completed:", "total:",
	} {
		if strings.Contains(lower, banned) {
			t.Errorf("Overview still shows %q:\n%s", banned, lower)
		}
	}
}

// Percentiles need a sample size, and both groups state theirs: the counts line
// for the workflow distributions, and a suffix on the agent-run row.
func TestOverviewStatesEverySampleSize(t *testing.T) {
	a := newTestApp(140, 60)
	a.model.overviewTab = &OverviewTab{Durations: overviewTestDurations()}

	out := stripANSI(a.renderOverviewTab(60))
	for _, want := range []string{"114 runs", "(1306 runs)"} {
		if !strings.Contains(out, want) {
			t.Errorf("Overview is missing the sample size %q:\n%s", want, out)
		}
	}
}

// A narrow terminal drops the maxima first and the p90s second. The wall/active
// pair and both sample sizes survive: they are what the block exists to say.
func TestOverviewDurationsDegradeWithWidth(t *testing.T) {
	d := overviewTestDurations()
	cases := []struct {
		width         int
		wantMax       bool
		wantP90       bool
		absentStrings []string
	}{
		{120, true, true, nil},
		// 42 inner columns hold the pair with its p90s, not with its maxima.
		{44, false, true, []string{"max "}},
		// 34 inner columns cannot hold a p90 line; the p50s and Ns stay.
		{36, false, false, []string{"max ", "p90 "}},
	}
	for _, tc := range cases {
		a := newTestApp(tc.width, 60)
		a.model.overviewTab = &OverviewTab{TasksDone: 105, TasksFailed: 9, Durations: d}
		out := stripANSI(a.renderOverviewTab(60))

		if got := strings.Contains(out, "max "); got != tc.wantMax {
			t.Errorf("width %d: max shown = %v, want %v:\n%s", tc.width, got, tc.wantMax, out)
		}
		if got := strings.Contains(out, "p90 "); got != tc.wantP90 {
			t.Errorf("width %d: p90 shown = %v, want %v:\n%s", tc.width, got, tc.wantP90, out)
		}
		for _, banned := range tc.absentStrings {
			if strings.Contains(out, banned) {
				t.Errorf("width %d: %q should have been dropped:\n%s", tc.width, banned, out)
			}
		}
		for _, want := range []string{"Wall:", "Active:", "114 runs", "(1306 runs)"} {
			if !strings.Contains(out, want) {
				t.Errorf("width %d: dropped %q:\n%s", tc.width, want, out)
			}
		}
	}

	// Framing survives every width the box is willing to draw.
	assertFramed(t, func() string {
		a := newTestApp(40, 60)
		a.model.overviewTab = &OverviewTab{Durations: d}
		return a.renderOverviewTab(60)
	}(), 40)
}

// "Nothing finished" is a claim about the window; a failed query is not that
// claim, so the two states read differently.
func TestOverviewDurationEmptyAndUnavailableStates(t *testing.T) {
	a := newTestApp(140, 60)
	a.model.overviewTab = &OverviewTab{Durations: &db.ActivityDurations{}}
	out := a.renderOverviewTab(60)
	plain := stripANSI(out)
	if !strings.Contains(plain, "no finished runs last 7 days") {
		t.Errorf("empty window should say so:\n%s", plain)
	}
	if !strings.Contains(out, StyleFooterDim.Render("  no finished runs last 7 days")) {
		t.Errorf("the empty state should be dimmed, not dressed as a figure:\n%q", out)
	}
	if strings.Contains(plain, "p50") {
		t.Errorf("empty window printed a distribution:\n%s", plain)
	}

	a.model.overviewTab = &OverviewTab{} // Durations nil: the query failed
	plain = stripANSI(a.renderOverviewTab(60))
	if !strings.Contains(plain, "durations unavailable") {
		t.Errorf("a failed query should say durations are unavailable:\n%s", plain)
	}
	if strings.Contains(plain, "no finished runs") {
		t.Errorf("unknown must not read as an empty window:\n%s", plain)
	}
}

// The fetch is what connects the selected window to the store. The Overview
// must read the distributions with the same `since` it reports back, or the
// block would describe a different stretch of time than its header.
func TestFetchOverviewCarriesActivityDurations(t *testing.T) {
	ctx := context.Background()
	c, err := db.New(ctx, filepath.Join(t.TempDir(), "overview-durations.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	// One finished workflow run with two ten-minute steps, plus one finished
	// agent execution.
	seedDurations(t, c)
	exec, err := c.CreateExecution(ctx, "task-1", "engineer", "t", "#1", "", "m", "cli", 1)
	if err != nil {
		t.Fatalf("create execution: %v", err)
	}
	completed := time.Now()
	exec.Status = "success"
	exec.DurationMs = (10 * time.Minute).Milliseconds()
	exec.CompletedAt = &completed
	if err := c.UpdateExecution(ctx, exec); err != nil {
		t.Fatalf("finish execution: %v", err)
	}

	a := newTestApp(140, 60)
	a.dbConn = c

	before := time.Now()
	msg := a.fetchOverview()().(overviewDataMsg)
	after := time.Now()

	d := msg.data.Durations
	if d == nil {
		t.Fatal("fetchOverview did not attach the activity durations")
	}
	if d.Instances.N != 1 || d.Active.N != 1 || d.Runs.N != 1 {
		t.Errorf("sample sizes = %d instances / %d active / %d runs, want 1/1/1", d.Instances.N, d.Active.N, d.Runs.N)
	}
	if got := d.Active.P50; got < 19*time.Minute || got > 21*time.Minute {
		t.Errorf("active p50 = %v, want the seeded 20m of step time", got)
	}
	if got := d.Runs.P50; got < 9*time.Minute || got > 11*time.Minute {
		t.Errorf("agent-run p50 = %v, want the seeded 10m execution", got)
	}
	if lo, hi := MetricsWindowLast7Days.Start(before), MetricsWindowLast7Days.Start(after); msg.since.Before(lo) || msg.since.After(hi) {
		t.Errorf("since = %v, want the window the header names (%v..%v)", msg.since, lo, hi)
	}

	// And the render actually shows them.
	a.Update(msg)
	out := stripANSI(a.renderOverviewTab(60))
	for _, want := range []string{"Finished (last 7 days):  1 run", "Active:  p50 20m", "(1 run)"} {
		if !strings.Contains(out, want) {
			t.Errorf("Overview is missing %q after a fetch:\n%s", want, out)
		}
	}
}

// The duration formatter is what stops a five-hour task reading as "21600s".
func TestFormatMetricDuration(t *testing.T) {
	cases := []struct {
		ms   int64
		want string
	}{
		{0, "—"},
		{42_000, "42s"},
		{90_000, "1.5m"},
		{3_600_000, "1.0h"},
		{21_600_000, "6.0h"},
	}
	for _, tc := range cases {
		if got := formatMetricDuration(tc.ms); got != tc.want {
			t.Errorf("formatMetricDuration(%d) = %q, want %q", tc.ms, got, tc.want)
		}
	}
}
