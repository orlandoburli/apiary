package dashboard

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
)

// activityTestConfig is the config the Workflows tab reads its rows from: one
// workflow that ran recently, with three steps of which only two were reached,
// and one workflow that did not run at all.
func activityTestConfig() *config.Config {
	return &config.Config{Workflows: []config.WorkflowConfig{
		{
			ID:          "impl",
			Description: "implementation",
			Steps: []config.StepConfig{
				{ID: "implement", Type: "agent", Agent: "engineer"},
				{ID: "review", Type: "agent", Agent: "reviewer"},
				{ID: "unreached", Type: "agent", Agent: "engineer"},
			},
		},
		{ID: "nightly-audit", Description: "audit"},
	}}
}

// seedDurations writes one finished instance of "impl" whose wall life is two
// hours and whose two steps ran ten minutes each, separated by a gap. Active
// time is therefore 20 minutes against a 120-minute wall — the pair the tab
// exists to compare — and the long tail after the last step is exactly what a
// mean would hide.
//
// Creation is two hours back; the store finishes the instance now, so the wall
// life is the elapsed two hours. The step timestamps are fixed offsets from
// creation, so their figures are exact instead of depending on when the suite
// runs, and two hours ago is inside the default seven-day window.
func seedDurations(t *testing.T, c *db.Client) {
	t.Helper()
	ctx := context.Background()

	start := time.Now().Add(-2 * time.Hour)
	implStart, implFinish := start.Add(5*time.Minute), start.Add(15*time.Minute)
	reviewStart, reviewFinish := start.Add(20*time.Minute), start.Add(30*time.Minute)

	if err := c.CreateWorkflowInstance(ctx, &db.WorkflowInstance{
		ID: "i1", WorkflowID: "impl", CellID: "i1", State: db.InstanceStateDone,
		CreatedAt: start,
	}); err != nil {
		t.Fatalf("create instance: %v", err)
	}

	for _, sr := range []*db.StepRun{
		{ID: "i1:implement", WorkflowInstanceID: "i1", StepID: "implement", AgentID: "engineer",
			State: "done", StartedAt: &implStart, FinishedAt: &implFinish},
		{ID: "i1:review", WorkflowInstanceID: "i1", StepID: "review", AgentID: "reviewer",
			State: "done", StartedAt: &reviewStart, FinishedAt: &reviewFinish},
	} {
		if err := c.CreateStepRun(ctx, sr); err != nil {
			t.Fatalf("create step run %s: %v", sr.ID, err)
		}
	}
}

// The Workflows tab answers "how long does a run take", not "how many ran": the
// selected workflow gets a distribution for both durations, the list gets a
// comparable p50 pair, and each step that ran gets its own distribution.
func TestWorkflowsTabShowsDurations(t *testing.T) {
	ctx := context.Background()
	c, err := db.New(ctx, filepath.Join(t.TempDir(), "wf-durations.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	seedDurations(t, c)

	a := newTestApp(120, 40)
	a.cfg = activityTestConfig()
	a.dbConn = c

	msg, ok := a.fetchWorkflowsConfig()().(workflowsConfigMsg)
	if !ok {
		t.Fatal("fetchWorkflowsConfig did not return a workflowsConfigMsg")
	}
	a.Update(msg)

	wf := msg.workflows[0]
	d := wf.Durations
	if d == nil || d.Runs != 1 {
		t.Fatalf("impl durations = %+v, want one finished run", d)
	}
	// The seeded run's whole point: waits make the wall life longer than the
	// time something was executing. (The steps are disjoint, so active is just
	// their sum here; union-vs-sum is the store's own test.)
	if !(d.Active.P50 < d.Wall.P50) {
		t.Fatalf("seeded active p50 %v should be below wall p50 %v", d.Active.P50, d.Wall.P50)
	}

	plain := stripANSI(a.renderWorkflowsTab(40))

	// The seeded figures rendered, the smaller one first: 20m of execution
	// inside a two-hour wall life.
	if !strings.Contains(plain, "active p50 20m") || !strings.Contains(plain, "wall p50 2h") {
		t.Errorf("want active p50 20m below wall p50 2h:\n%s", plain)
	}

	// The rendered figures are the fetched ones, not a re-derivation.
	// At 120 columns the full layout (with maxima) fits.
	for _, want := range []string{
		fmt.Sprintf("active p50 %s · p90 %s · max %s",
			formatCompactDuration(d.Active.P50), formatCompactDuration(d.Active.P90), formatCompactDuration(d.Active.Max)),
		fmt.Sprintf("wall p50 %s · p90 %s · max %s",
			formatCompactDuration(d.Wall.P50), formatCompactDuration(d.Wall.P90), formatCompactDuration(d.Wall.Max)),
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("summary missing %q:\n%s", want, plain)
		}
	}
	// Active is put first: the execution is the interesting half, the wall the
	// context it happened in.
	if ai, wi := strings.Index(plain, "active p50"), strings.Index(plain, "wall p50"); ai < 0 || wi < 0 || ai > wi {
		t.Errorf("active and wall distributions not shown in that order:\n%s", plain)
	}

	// The left panel's comparable pair, in the same order.
	activeP50, wallP50 := formatCompactDuration(d.Active.P50), formatCompactDuration(d.Wall.P50)
	if want := fmt.Sprintf("1 · %s/%s", activeP50, wallP50); !strings.Contains(plain, want) {
		t.Errorf("left hint missing %q:\n%s", want, plain)
	}

	// Per step: its own interval only, no active/wall split. Both steps took
	// ten minutes, and each has its own line.
	if got := strings.Count(plain, "ran 1 · p50 10.0m · p90 10.0m"); got != 2 {
		t.Errorf("want a duration line per reached step, found %d:\n%s", got, plain)
	}
	// A step nobody reached must not grow a line of zeros.
	if idx := strings.Index(plain, "unreached"); idx >= 0 {
		if strings.Contains(plain[idx:], " ran ") {
			t.Errorf("unreached step should have no duration line:\n%s", plain)
		}
	} else {
		t.Errorf("unreached step not rendered:\n%s", plain)
	}
}

// The summary is the whole comparison on one line, so its format is a contract:
// run count, then active, then wall, with the tail. Widths drop the maxima
// first and the p90s second, but never the count or the active/wall split.
func TestWorkflowDurationsSummaryFormat(t *testing.T) {
	d := &db.WorkflowDurations{
		WorkflowID: "implementation",
		Runs:       114,
		Active:     db.DurationStats{N: 114, P50: 54 * time.Minute, P90: 2*time.Hour + 36*time.Minute, Max: 13 * time.Hour},
		Wall:       db.DurationStats{N: 114, P50: 4*time.Hour + 24*time.Minute, P90: 21*time.Hour + 30*time.Minute, Max: 47*time.Hour + 48*time.Minute},
	}
	at := func(width int) string {
		return stripANSI(workflowDurationsSummary(d, false, MetricsWindowLast7Days, width))
	}

	cases := []struct {
		width int
		want  string
	}{
		{200, "  114 runs · active p50 54m · p90 2.6h · max 13h — wall p50 4.4h · p90 21.5h · max 47.8h"},
		{80, "  114 runs · active p50 54m · p90 2.6h — wall p50 4.4h · p90 21.5h"},
		{40, "  114 runs · active p50 54m — wall p50 4.4h"},
		// Even at a width where nothing fits, the count and both p50s stay.
		{1, "  114 runs · active p50 54m — wall p50 4.4h"},
	}
	for _, tc := range cases {
		if got := at(tc.width); got != tc.want {
			t.Errorf("width %d:\n got %q\nwant %q", tc.width, got, tc.want)
		}
	}

	// A mean would hide the tail this line exists to show: no average figure.
	if strings.Contains(at(200), "avg") {
		t.Errorf("summary shows an average instead of percentiles: %q", at(200))
	}

	// One run is singular, and a workflow with nothing finished keeps the
	// dimmed empty state rather than a row of zero-second runs.
	one := stripANSI(workflowDurationsSummary(&db.WorkflowDurations{
		Runs: 1, Active: db.DurationStats{N: 1, P50: time.Minute}, Wall: db.DurationStats{N: 1, P50: 2 * time.Minute},
	}, false, MetricsWindowLast7Days, 200))
	if !strings.Contains(one, "1 run ·") {
		t.Errorf("single run not singular: %q", one)
	}
	if none := stripANSI(workflowDurationsSummary(nil, false, MetricsWindowLast30Days, 200)); none != "  no runs last 30 days" {
		t.Errorf("empty state = %q", none)
	}
	if unavailable := stripANSI(workflowDurationsSummary(d, true, MetricsWindowLast7Days, 200)); !strings.Contains(unavailable, "metrics unavailable") {
		t.Errorf("failed query did not say so: %q", unavailable)
	}
}

// The left hint is a comparison between rows, so it is the count and the two
// p50s — and when even that does not fit, the count alone rather than a clipped
// number that reads as a different figure.
func TestWorkflowDurationHint(t *testing.T) {
	d := &db.WorkflowDurations{
		Runs:   114,
		Active: db.DurationStats{P50: 54 * time.Minute},
		Wall:   db.DurationStats{P50: 4*time.Hour + 24*time.Minute},
	}
	// 23 columns is what a 28-column panel leaves beside its 5-space indent.
	if got := workflowDurationHint(d, 23); got != "114 · 54m/4.4h" {
		t.Errorf("hint = %q, want the p50 pair", got)
	}
	if got := workflowDurationHint(d, 6); got != "114" {
		t.Errorf("narrow hint = %q, want the run count alone", got)
	}
	if got := workflowDurationHint(nil, 23); got != "" {
		t.Errorf("nil durations produced a hint: %q", got)
	}
	if got := workflowDurationHint(&db.WorkflowDurations{Runs: 0}, 23); got != "" {
		t.Errorf("zero runs produced a hint: %q", got)
	}
	// A 24-character workflow id leaves room beside it for the hint line.
	if lipgloss.Width("114 · 54m/4.4h") > 28-5 {
		t.Error("the compact hint does not fit the left panel")
	}
}

// A workflow that finished nothing in the window shows a dimmed
// "no runs last 7 days" rather than a row of zeros — absence of a figure is not
// an instantaneous run.
func TestWorkflowsTabNeverRunShowsDimmedEmptyState(t *testing.T) {
	ctx := context.Background()
	c, err := db.New(ctx, filepath.Join(t.TempDir(), "wf-idle.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	seedDurations(t, c)

	a := newTestApp(120, 40)
	a.cfg = activityTestConfig()
	a.dbConn = c
	a.Update(a.fetchWorkflowsConfig()())

	// Select the workflow with no instances at all.
	a.model.workflowsTab.SelectedIdx = 1
	out := a.renderWorkflowsTab(40)
	plain := stripANSI(out)

	if !strings.Contains(plain, "no runs last 7 days") {
		t.Fatalf("idle workflow should say it did not run in the window:\n%s", plain)
	}
	// The empty state is dimmed, not dressed as a statistic.
	if !strings.Contains(out, StyleFooterDim.Render("no runs last 7 days")) {
		t.Errorf("empty state should use the dim style:\n%q", out)
	}
	if strings.Contains(plain, "0 runs") {
		t.Errorf("idle workflow printed a row of zeros:\n%s", plain)
	}
}

// A failed durations query must cost the tab its figures, never its rows.
func TestWorkflowsTabMetricsUnavailableStillRendersDefinitions(t *testing.T) {
	ctx := context.Background()
	c, err := db.New(ctx, filepath.Join(t.TempDir(), "wf-closed.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	_ = c.Close() // every query from here on fails

	a := newTestApp(120, 40)
	a.cfg = activityTestConfig()
	a.dbConn = c
	a.Update(a.fetchWorkflowsConfig()())

	plain := stripANSI(a.renderWorkflowsTab(40))
	for _, want := range []string{"impl", "implement", "metrics unavailable"} {
		if !strings.Contains(plain, want) {
			t.Errorf("Workflows tab missing %q after a durations failure:\n%s", want, plain)
		}
	}
	// Unknown is not "no runs": the empty state would be a claim the query
	// could not make.
	if strings.Contains(plain, "no runs last 7 days") {
		t.Errorf("a failed query must not read as an idle workflow:\n%s", plain)
	}
}
