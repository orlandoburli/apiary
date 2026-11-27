package dashboard

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// seedActivity writes one completed instance and two step runs: a successful
// step with a five-minute duration, and a failed step whose start and finish are
// the same instant.
//
// The step timestamps sit at local midnight plus a fixed offset rather than
// "now minus an hour": a fixed offset keeps the expected average deterministic
// even when the test starts at 00:00:01, and local midnight is inside the
// default seven-day window whatever time the suite runs. The instance is
// stamped by the store with the wall clock, so its average is only asserted as
// "present", not as an exact value.
func seedActivity(t *testing.T, c *db.Client) {
	t.Helper()
	ctx := context.Background()

	now := time.Now()
	since := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	stepEnd := since.Add(5 * time.Minute)

	if err := c.CreateWorkflowInstance(ctx, &db.WorkflowInstance{
		ID: "i1", WorkflowID: "impl", CellID: "i1", State: db.InstanceStateDone, CreatedAt: since,
	}); err != nil {
		t.Fatalf("create instance: %v", err)
	}

	for _, sr := range []*db.StepRun{
		{ID: "i1:implement", WorkflowInstanceID: "i1", StepID: "implement", AgentID: "engineer",
			State: "done", StartedAt: &since, FinishedAt: &stepEnd},
		{ID: "i1:review", WorkflowInstanceID: "i1", StepID: "review", AgentID: "reviewer",
			State: "failed", StartedAt: &since, FinishedAt: &since},
	} {
		if err := c.CreateStepRun(ctx, sr); err != nil {
			t.Fatalf("create step run %s: %v", sr.ID, err)
		}
	}
}

// The Workflows tab is where an operator asks "did my workflows run?": the list
// carries a compact hint, the selected workflow a spelled-out summary naming the
// window, and each step that was reached one muted stats line.
func TestWorkflowsTabShowsWindowActivity(t *testing.T) {
	ctx := context.Background()
	c, err := db.New(ctx, filepath.Join(t.TempDir(), "wf-activity.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	seedActivity(t, c)

	a := newTestApp(120, 40)
	a.cfg = activityTestConfig()
	a.dbConn = c

	a.Update(a.fetchWorkflowsConfig()())
	out := a.renderWorkflowsTab(40)
	plain := stripANSI(out)

	for _, want := range []string{
		"impl", "implementation",
		"Last 7 days: 1 run · 1 completed · 0 failed",
		"1·1✓·0✗",                            // the compact left-panel hint
		"ran 1 · 1 ok · 0 failed · avg 5.0m", // implement
		"ran 1 · 0 ok · 1 failed",            // review: finished in the same instant
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("Workflows tab missing %q:\n%s", want, plain)
		}
	}

	// A step nobody reached must not grow a line of zeros.
	if idx := strings.Index(plain, "unreached"); idx >= 0 {
		if strings.Contains(plain[idx:], " ran ") {
			t.Errorf("unreached step should have no stats line:\n%s", plain)
		}
	} else {
		t.Errorf("unreached step not rendered:\n%s", plain)
	}
	// A zero duration is unknown, not instantaneous: no "0s" average.
	if strings.Contains(plain, "avg 0s") {
		t.Errorf("zero-duration step printed an average of 0s:\n%s", plain)
	}
}

// The selected workflow's summary carries the average duration, which the left
// panel only has room to abbreviate.
func TestWorkflowSummaryLineShowsAverage(t *testing.T) {
	line := stripANSI(workflowSummaryLine(&WorkflowActivity{
		Runs: 12, Completed: 10, Failed: 1, AvgDurationMs: 45 * 60 * 1000,
	}, false, MetricsWindowLast7Days))
	for _, want := range []string{"Last 7 days: 12 runs", "10 completed", "1 failed", "avg 45.0m"} {
		if !strings.Contains(line, want) {
			t.Errorf("summary missing %q: %q", want, line)
		}
	}
}

// A workflow that did not start anything in the window shows a dimmed
// "no runs last 7 days" rather than a row of zeros — absence of a stat is not
// zero.
func TestWorkflowsTabNeverRunShowsDimmedEmptyState(t *testing.T) {
	ctx := context.Background()
	c, err := db.New(ctx, filepath.Join(t.TempDir(), "wf-idle.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	seedActivity(t, c)

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
	if !strings.Contains(plain, "no runs last 7 days") {
		t.Fatalf("idle workflow summary missing:\n%s", plain)
	}
	// The empty state is dimmed, not dressed as a statistic.
	if !strings.Contains(out, StyleFooterDim.Render("no runs last 7 days")) {
		t.Errorf("empty state should use the dim style:\n%q", out)
	}
	if strings.Contains(plain, "Last 7 days: 0 runs") {
		t.Errorf("idle workflow printed a row of zeros:\n%s", plain)
	}
}

// A failed metrics query must cost the tab its stats, never its rows.
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
			t.Errorf("Workflows tab missing %q after a metrics failure:\n%s", want, plain)
		}
	}
	// Unknown is not "no runs": the empty state would be a claim the query
	// could not make.
	if strings.Contains(plain, "no runs last 7 days") {
		t.Errorf("a failed query must not read as an idle workflow:\n%s", plain)
	}
}
