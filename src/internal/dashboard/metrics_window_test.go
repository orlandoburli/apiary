package dashboard

import (
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/db"
)

// The window is the only thing that scopes the Overview and Workflows figures,
// so its vocabulary is a contract: these are the words the headers print, the
// docs use, and an operator learns. A silent rename would leave old screenshots
// and habits pointing at a window that no longer exists.
func TestMetricsWindowLabels(t *testing.T) {
	cases := []struct {
		w     MetricsWindow
		label string
		short string
	}{
		{MetricsWindowLast7Days, "last 7 days", "7d"},
		{MetricsWindowLast30Days, "last 30 days", "30d"},
		{MetricsWindowLast90Days, "last 90 days", "90d"},
		{MetricsWindowAllTime, "all time", "all"},
	}
	for _, tc := range cases {
		if got := tc.w.Label(); got != tc.label {
			t.Errorf("Label() = %q, want %q", got, tc.label)
		}
		if got := tc.w.ShortLabel(); got != tc.short {
			t.Errorf("ShortLabel() = %q, want %q", got, tc.short)
		}
	}

	// A week is the default: long enough to show a rhythm, short enough to be
	// about the current work.
	if got := NewModel().metricsWindow; got != MetricsWindowLast7Days {
		t.Errorf("default window = %q, want last 7 days", got.Label())
	}
}

// Start is the one mapping from a window to the lower bound the store queries
// get. A fixed now pins it: these are exact hour counts, not calendar
// arithmetic, so a 7-day window is always 168 hours whatever the month or
// daylight-saving boundary in between.
func TestMetricsWindowStart(t *testing.T) {
	now := time.Date(2026, 11, 27, 9, 30, 0, 0, time.Local)

	cases := []struct {
		w    MetricsWindow
		want time.Time
	}{
		{MetricsWindowLast7Days, now.Add(-7 * 24 * time.Hour)},
		{MetricsWindowLast30Days, now.Add(-30 * 24 * time.Hour)},
		{MetricsWindowLast90Days, now.Add(-90 * 24 * time.Hour)},
		// All time is the zero time: that is how the store is told there is no
		// lower bound. "Now minus a very long time" would not do — the rate
		// denominator would be two thousand years.
		{MetricsWindowAllTime, time.Time{}},
	}
	for _, tc := range cases {
		if got := tc.w.Start(now); !got.Equal(tc.want) {
			t.Errorf("%q Start(%v) = %v, want %v", tc.w.Label(), now, got, tc.want)
		}
	}
	if !MetricsWindowAllTime.Start(now).IsZero() {
		t.Error("all time must resolve to the zero time, not a distant start")
	}
}

// `w` widens the window one step at a time and wraps: four presses return to
// where it started, so the key cannot get stuck on a window the operator did
// not mean to keep.
func TestMetricsWindowCyclesThroughAllFour(t *testing.T) {
	a := newTestApp(100, 30)

	var got []string
	for i := 0; i < 4; i++ {
		got = append(got, a.model.metricsWindow.Label())
		a.Update(keyPress("w"))
	}
	want := []string{"last 7 days", "last 30 days", "last 90 days", "all time"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("cycle = %v, want %v", got, want)
		}
	}
	if got := a.model.metricsWindow; got != MetricsWindowLast7Days {
		t.Errorf("after four presses window = %q, want it back at last 7 days", got.Label())
	}
}

// Pressing `w` must also refetch: the figures on screen were measured over the
// old window, and leaving them there while the header changes would make the
// numbers lie for one refresh cycle.
func TestMetricsWindowKeyRefetchesTheActiveTab(t *testing.T) {
	for _, tab := range []string{"Overview", "Workflows"} {
		a := newTestApp(100, 30)
		if !a.model.SetActiveTab(tab) {
			t.Fatalf("tab %q does not exist", tab)
		}

		_, cmd := a.Update(keyPress("w"))
		if cmd == nil {
			t.Errorf("%s: `w` must return a fetch command", tab)
		}
		if got := a.model.metricsWindow; got != MetricsWindowLast30Days {
			t.Errorf("%s: window = %q after one press, want last 30 days", tab, got.Label())
		}
	}
}

// The two tabs share one window: changing it on Overview and walking to
// Workflows must not silently reset it, and a press there must carry back.
func TestMetricsWindowPersistsAcrossTabs(t *testing.T) {
	a := newTestApp(120, 40)

	a.Update(keyPress("w")) // Overview: last 7 days → last 30 days
	if got := a.model.metricsWindow; got != MetricsWindowLast30Days {
		t.Fatalf("window after Overview press = %q, want last 30 days", got.Label())
	}

	if !a.model.SetActiveTab("Workflows") {
		t.Fatal("Workflows tab missing")
	}
	// No config, so the tab renders its empty state — the title still names the
	// window, which is what the switch had to preserve.
	if out := stripANSI(a.renderWorkflowsTab(40)); !strings.Contains(out, "last 30 days") {
		t.Errorf("Workflows tab did not keep the Overview's window:\n%s", out)
	}

	a.Update(keyPress("w")) // Workflows: last 30 days → last 90 days
	if got := a.model.metricsWindow; got != MetricsWindowLast90Days {
		t.Fatalf("window after Workflows press = %q, want last 90 days", got.Label())
	}

	a.model.SetActiveTab("Overview")
	if out := stripANSI(a.renderOverviewTab(40)); !strings.Contains(out, "last 90 days") {
		t.Errorf("Overview did not see the Workflows press:\n%s", out)
	}
}

// `w` already means something else on two other screens. Those meanings are
// tab-scoped and must survive: a global window cycle would quietly turn the
// Logs wrap toggle and the Agents worker stepper into window changes.
func TestMetricsWindowKeyDoesNotHijackOtherScreens(t *testing.T) {
	// Logs: `w` toggles wrapping and nothing else.
	a := newTestApp(100, 30)
	a.model.SetActiveTab("Logs")
	a.model.logsTab.Wrap = true
	a.Update(keyPress("w"))
	if a.model.logsTab.Wrap {
		t.Error("Logs: `w` no longer toggles word wrap")
	}
	if got := a.model.metricsWindow; got != MetricsWindowLast7Days {
		t.Errorf("Logs: window changed to %q", got.Label())
	}

	// Agents detail: `w` cycles max_workers.
	a = newTestApp(100, 30)
	a.model.SetActiveTab("Agents")
	a.model.agentsTab.View = AgentViewDetail
	a.model.agentsTab.Detail = &AgentStatus{ID: "engineer", MaxWorkers: 2}
	a.Update(keyPress("w"))
	if got := a.model.agentsTab.Detail.MaxWorkers; got != 3 {
		t.Errorf("Agents detail: max_workers = %d after `w`, want 3", got)
	}
	if got := a.model.metricsWindow; got != MetricsWindowLast7Days {
		t.Errorf("Agents detail: window changed to %q", got.Label())
	}
}

// An overlay owns the keyboard while it is open. `w` typed into a form must be
// a character, not a window change, so the cycle sits below every overlay guard.
func TestMetricsWindowKeyInertWhileOverlayOwnsKeyboard(t *testing.T) {
	t.Run("task filter", func(t *testing.T) {
		a := newTestApp(100, 30)
		a.model.SetActiveTab("Tasks")
		a.model.tasksTab.FilterActive = true
		a.Update(keyPress("w"))
		if got := a.model.metricsWindow; got != MetricsWindowLast7Days {
			t.Errorf("window changed to %q while filtering", got.Label())
		}
		if !strings.Contains(a.model.tasksTab.FilterText, "w") {
			t.Errorf("filter text = %q, want the typed `w`", a.model.tasksTab.FilterText)
		}
	})

	t.Run("confirm", func(t *testing.T) {
		a := newTestApp(100, 30)
		a.model.SetActiveTab("Overview")
		a.model.confirmAction = "restart"
		a.Update(keyPress("w"))
		if got := a.model.metricsWindow; got != MetricsWindowLast7Days {
			t.Errorf("window changed to %q while a confirm modal was open", got.Label())
		}
	})

	t.Run("workflow picker", func(t *testing.T) {
		a := newTestApp(100, 30)
		a.model.SetActiveTab("Overview")
		a.model.pickerActive = true
		a.Update(keyPress("w"))
		if got := a.model.metricsWindow; got != MetricsWindowLast7Days {
			t.Errorf("window changed to %q while the picker was open", got.Label())
		}
	})

	t.Run("approval sheet", func(t *testing.T) {
		a := newTestApp(100, 30)
		a.model.SetActiveTab("Overview")
		a.openApprovalForm(&db.ApprovalRequest{ID: "wf:gate"})
		a.Update(keyPress("w"))
		if !a.model.approvalActive {
			t.Fatal("`w` closed the approval sheet")
		}
		if got := a.model.metricsWindow; got != MetricsWindowLast7Days {
			t.Errorf("window changed to %q while the approval sheet was open", got.Label())
		}
	})

	t.Run("question sheet", func(t *testing.T) {
		a := newTestApp(100, 30)
		a.model.SetActiveTab("Overview")
		a.openQuestion(db.AgentQuestion{
			ID:       "q1",
			Question: "Which branch?",
			Options:  []db.QuestionOption{{Value: "main"}, {Value: "release"}},
		})
		a.Update(keyPress("w"))
		if !a.model.questionActive {
			t.Fatal("`w` closed the question sheet")
		}
		if got := a.model.metricsWindow; got != MetricsWindowLast7Days {
			t.Errorf("window changed to %q while the question sheet was open", got.Label())
		}
	})
}

// Every figure carries its window. The header is the promise that the numbers
// below it mean something specific, so each of the three Overview blocks and
// the Workflows summary must state the active one.
func TestMetricsWindowAppearsInHeaders(t *testing.T) {
	a := newTestApp(140, 60)
	a.model.metricsWindow = MetricsWindowLast30Days
	a.model.overviewTab = &OverviewTab{Status: "Healthy", CompletedToday: 7, RunsTotal: 9}

	out := stripANSI(a.renderOverviewTab(60))
	for _, want := range []string{
		"OVERVIEW · last 30 days",
		"Tasks (last 30 days)",
		"Runs (last 30 days)",
		"Usage (last 30 days)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Overview header missing %q:\n%s", want, out)
		}
	}
	// The old fixed window must be gone, not just supplemented.
	if strings.Contains(out, "since midnight") {
		t.Errorf("Overview still claims a fixed midnight window:\n%s", out)
	}

	// All time is a window like any other and is named as such.
	a.model.metricsWindow = MetricsWindowAllTime
	if out := stripANSI(a.renderOverviewTab(60)); !strings.Contains(out, "Tasks (all time)") {
		t.Errorf("Overview did not name the all-time window:\n%s", out)
	}

	// The Workflows tab names the window in its title; the distribution line
	// below belongs to that window by position, and the empty state repeats it
	// in words for a workflow that finished nothing.
	a.model.workflowsTab = &WorkflowsTab{Workflows: []WorkflowConfigItem{{ID: "impl"}}}
	a.model.SetActiveTab("Workflows")
	if out := stripANSI(a.renderWorkflowsTab(40)); !strings.Contains(out, "WORKFLOWS · all time") {
		t.Errorf("Workflows title did not name the all-time window:\n%s", out)
	}
	if out := stripANSI(a.renderWorkflowsTab(40)); !strings.Contains(out, "no runs all time") {
		t.Errorf("Workflows empty state did not name the window:\n%s", out)
	}
}

// The header and the query must be the same decision. If the fetch resolved its
// own start from a different window, a header could name 30 days over figures
// measured over 7 — the exact failure a selectable window invites.
func TestMetricsFetchUsesTheWindowTheHeaderNames(t *testing.T) {
	a := newTestApp(140, 60)
	a.model.metricsWindow = MetricsWindowLast30Days

	before := time.Now()
	ov, ok := a.fetchOverview()().(overviewDataMsg)
	after := time.Now()
	if !ok {
		t.Fatal("fetchOverview did not return an overviewDataMsg")
	}
	if ov.window != MetricsWindowLast30Days {
		t.Errorf("overview message window = %q, want last 30 days", ov.window.Label())
	}
	if lo, hi := MetricsWindowLast30Days.Start(before), MetricsWindowLast30Days.Start(after); ov.since.Before(lo) || ov.since.After(hi) {
		t.Errorf("overview since = %v, want between %v and %v", ov.since, lo, hi)
	}

	wf, ok := a.fetchWorkflowsConfig()().(workflowsConfigMsg)
	if !ok {
		t.Fatal("fetchWorkflowsConfig did not return a workflowsConfigMsg")
	}
	if wf.window != MetricsWindowLast30Days {
		t.Errorf("workflows message window = %q, want last 30 days", wf.window.Label())
	}
	if wf.since.IsZero() {
		t.Error("workflows since is zero for a 30-day window")
	}

	// All time is passed through as the zero time: the store recognises it and
	// resolves the rate denominator itself.
	a.model.metricsWindow = MetricsWindowAllTime
	ov = a.fetchOverview()().(overviewDataMsg)
	wf = a.fetchWorkflowsConfig()().(workflowsConfigMsg)
	if !ov.since.IsZero() || !wf.since.IsZero() {
		t.Errorf("all time should be a zero start, got overview %v / workflows %v", ov.since, wf.since)
	}
}

// A fetch already in flight when the window changes comes back with the old
// window's figures. Applying it would print them under the new header, so it is
// dropped: the `w` press asked for its own fetch.
func TestStaleMetricsWindowResultIsDropped(t *testing.T) {
	a := newTestApp(100, 30)
	a.Update(overviewDataMsg{data: OverviewTab{CompletedToday: 11}, window: MetricsWindowLast7Days})
	if got := a.model.overviewTab.CompletedToday; got != 11 {
		t.Fatalf("matching window result not applied: CompletedToday = %d", got)
	}

	a.Update(overviewDataMsg{data: OverviewTab{CompletedToday: 99}, window: MetricsWindowLast30Days})
	if got := a.model.overviewTab.CompletedToday; got != 11 {
		t.Errorf("stale 30-day result applied under a 7-day window: CompletedToday = %d", got)
	}

	a.model.workflowsTab.Workflows = []WorkflowConfigItem{{ID: "impl"}}
	a.Update(workflowsConfigMsg{workflows: []WorkflowConfigItem{{ID: "other"}}, window: MetricsWindowLast90Days})
	if got := a.model.workflowsTab.Workflows[0].ID; got != "impl" {
		t.Errorf("stale workflow activity applied under a 7-day window: %q", got)
	}
}

// The footer is how the key is discoverable: it is tab-scoped, so the Logs
// wrap toggle and the window cycle can share a letter without confusing either
// screen.
func TestWindowKeyInFooterOnMetricsTabs(t *testing.T) {
	a := newTestApp(140, 40)
	for _, tab := range []string{"Overview", "Workflows"} {
		a.model.SetActiveTab(tab)
		found := false
		for _, f := range a.footerKeys() {
			if f.k == "w" && f.d == "window" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s footer is missing the `w window` hint", tab)
		}
	}

	a.model.SetActiveTab("Overview")
	if out := stripANSI(a.renderFooter()); !strings.Contains(out, "window") {
		t.Errorf("Overview footer does not show the window hint:\n%s", out)
	}

	// Logs keeps its own label for the same key.
	a.model.SetActiveTab("Logs")
	for _, f := range a.footerKeys() {
		if f.k == "w" && f.d == "window" {
			t.Error("Logs footer now advertises the window cycle")
		}
	}
}

// The window label must not break the frame when the terminal is narrow: the
// box title falls back to a short form, and the body headers keeps spelling the
// window out, so nothing is clipped into nonsense.
func TestMetricsWindowTitleFitsNarrowTerminals(t *testing.T) {
	for _, width := range []int{120, 80, 40, 24} {
		a := newTestApp(width, 30)
		a.model.overviewTab = &OverviewTab{Status: "Healthy"}
		a.model.workflowsTab = &WorkflowsTab{Workflows: []WorkflowConfigItem{{ID: "impl"}}}

		assertFramed(t, a.renderOverviewTab(30), width)
		assertFramed(t, a.renderWorkflowsTab(30), width)

		// Below the point where the full phrase fits, the title is shortened
		// rather than allowed to push the top border past the terminal edge.
		if width == 24 {
			if out := stripANSI(a.renderOverviewTab(30)); !strings.Contains(out, "OVERVIEW · 7d") {
				t.Errorf("narrow Overview title did not shorten:\n%s", out)
			}
		}
	}
}
