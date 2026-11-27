package dashboard

import (
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/db"
)

// metricsHelpCopyLines is the explanation exactly as an operator reads it at a
// terminal wide enough not to reflow it. It is duplicated here on purpose: this
// copy is the feature, and a test that read it out of the implementation would
// pass even if the definitions were reworded or dropped.
var metricsHelpCopyLines = []string{
	"Metrics help",
	"Window (w)    last 7 days · 30 days · 90 days · all time. Every figure covers",
	"              runs that finished inside it.",
	"Wall          The run's whole life, start to finish. Includes agent work, CI",
	"              polling, human approvals, blockers and retry backoff. Answers",
	"              \"when will this be done?\"",
	"Active        The union of the run's step intervals: time something was",
	"              actually executing. Waits are excluded and overlapping steps",
	"              count once. Answers \"how much work is this?\"",
	"Agents        One agent process, its own duration. No waiting.",
	"p50 / p90     Nearest-rank percentiles. p50: half the runs took at most this",
	"              long. p90: 90% did. A p90 is a duration a run actually took.",
	"max           The slowest run in the window.",
	"N             The sample size (e.g. 114 runs). A percentile without N is not",
	"              interpretable.",
	"H or esc      close",
}

func TestMetricsHelpOpensAndClosesOnBothTabs(t *testing.T) {
	for _, tab := range []string{"Overview", "Workflows"} {
		t.Run(tab, func(t *testing.T) {
			a := newTestApp(120, 40)
			a.model.SetActiveTab(tab)

			a.Update(keyPress("H"))
			if !a.model.metricsHelpActive {
				t.Fatalf("%s: H did not open the metrics help", tab)
			}

			// H toggles it back off, and nothing is left scrolled for the next open.
			a.model.metricsHelpScroll = 2
			a.Update(keyPress("H"))
			if a.model.metricsHelpActive {
				t.Fatalf("%s: H did not close the metrics help", tab)
			}
			if a.model.metricsHelpScroll != 0 {
				t.Fatalf("%s: scroll = %d after close, want 0", tab, a.model.metricsHelpScroll)
			}

			a.Update(keyPress("H"))
			a.Update(keyPress("esc"))
			if a.model.metricsHelpActive {
				t.Fatalf("%s: esc did not close the metrics help", tab)
			}
		})
	}

	// The step panel keeps h/esc/arrows for its own navigation; H still opens the
	// explanation from either half of the Workflows tab.
	a := newTestApp(120, 40)
	a.model.SetActiveTab("Workflows")
	a.model.workflowsTab.Focus = WorkflowsViewSteps
	a.Update(keyPress("H"))
	if !a.model.metricsHelpActive {
		t.Fatal("H did not open the metrics help from the workflow step panel")
	}
}

// The overlay is scoped to the two tabs that print duration figures. On every
// other tab H is not a shortcut, so it must leave the model alone.
func TestMetricsHelpDoesNotOpenOnOtherTabs(t *testing.T) {
	for _, tab := range NewModel().tabs {
		if metricsHelpTab(tab) {
			continue
		}
		a := newTestApp(120, 40)
		a.model.SetActiveTab(tab)

		a.Update(keyPress("H"))

		if a.model.metricsHelpActive {
			t.Errorf("%s: H opened the metrics help; it belongs to Overview and Workflows only", tab)
		}
	}
}

// While the overlay is open it owns the keyboard: tab and the window key must
// not reach the tab underneath, or the figures being explained would change
// while the explanation is on screen.
func TestMetricsHelpOwnsTheKeyboardWhileOpen(t *testing.T) {
	a := newTestApp(120, 40)
	a.Update(keyPress("H"))

	beforeTab := a.model.ActiveTab()
	beforeWindow := a.model.metricsWindow
	a.Update(keyPress("tab"))
	a.Update(keyPress("w"))
	a.Update(keyPress("q"))

	if !a.model.metricsHelpActive {
		t.Fatal("q closed the overlay; only H and esc should")
	}
	if got := a.model.ActiveTab(); got != beforeTab {
		t.Errorf("tab = %q while help was open, want %q", got, beforeTab)
	}
	if got := a.model.metricsWindow; got != beforeWindow {
		t.Errorf("window = %q while help was open, want %q", got.Label(), beforeWindow.Label())
	}
}

// Nobody's half-typed form should be interrupted, and H must not be read as a
// shortcut underneath an overlay. Each case also proves the key still reaches
// the overlay that owns it, rather than being swallowed by the help guard.
func TestMetricsHelpInertWhileAnotherOverlayOwnsKeys(t *testing.T) {
	cases := []struct {
		name string
		set  func(*App)
		// after reports that the key reached the overlay that owns the screen,
		// rather than being swallowed by the help guard.
		after func(*App) bool
	}{
		{
			name: "task filter being typed",
			set: func(a *App) {
				a.model.SetActiveTab("Tasks")
				a.model.tasksTab.FilterActive = true
			},
			after: func(a *App) bool {
				return a.model.tasksTab.FilterActive && a.model.tasksTab.FilterText == "H"
			},
		},
		{
			name: "workflow picker",
			set: func(a *App) {
				a.model.workflowsTab.Workflows = []WorkflowConfigItem{{ID: "wf"}}
				a.model.pickerActive = true
			},
			after: func(a *App) bool { return a.model.pickerActive },
		},
		{
			name: "confirm modal",
			set: func(a *App) {
				a.model.confirmAction = "restart"
				a.model.confirmTaskID = "task-1"
			},
			// The confirm modal dismisses itself on any key that is not y, so
			// H having reached it looks like a cleared prompt.
			after: func(a *App) bool { return a.model.confirmAction == "" },
		},
		{
			name: "approval sheet",
			set: func(a *App) {
				a.model.approvalActive = true
				a.model.approvalReq = fieldRequest()
				a.model.approvalOutcomes = approvalFieldsOutcomesForTest()
			},
			after: func(a *App) bool { return a.model.approvalActive },
		},
		{
			name: "question sheet",
			set: func(a *App) {
				a.openQuestion(sampleQuestion("q-1", db.QuestionOption{Value: "yes"}))
			},
			after: func(a *App) bool { return a.model.questionActive },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestApp(120, 40)
			tc.set(a)

			a.Update(keyPress("H"))

			if a.model.metricsHelpActive {
				t.Fatalf("%s: H opened the metrics help over another overlay", tc.name)
			}
			if !tc.after(a) {
				t.Fatalf("%s: H did not reach the overlay that owns the screen", tc.name)
			}
		})
	}

	// In the filter, H is text: it must land in the query, not toggle anything.
	a := newTestApp(120, 40)
	a.model.SetActiveTab("Tasks")
	a.model.tasksTab.FilterActive = true
	a.Update(keyPress("H"))
	if got := a.model.tasksTab.FilterText; got != "H" {
		t.Fatalf("filter text = %q, want the H typed into it", got)
	}
	if a.model.metricsHelpActive {
		t.Fatal("the help overlay opened while a filter was being typed")
	}
}

// approvalFieldsOutcomesForTest mirrors what opening an approval sheet seeds.
func approvalFieldsOutcomesForTest() []db.ApprovalOutcome {
	return []db.ApprovalOutcome{{Value: "approve", Label: "Approve"}, {Value: "reject", Label: "Reject", Fail: true}}
}

// A question auto-open is the one overlay that appears on its own; it must not
// pull the operator out of the explanation they asked for.
func TestMetricsHelpNotYankedByQuestionPoll(t *testing.T) {
	a := newTestApp(120, 40)
	a.Update(keyPress("H"))

	a.Update(questionsMsg{questions: []db.AgentQuestion{sampleQuestion("q-1", db.QuestionOption{Value: "yes"})}})

	if !a.model.metricsHelpActive {
		t.Fatal("a polled question closed the metrics help")
	}
	if a.model.questionActive {
		t.Fatal("a polled question opened over the metrics help")
	}
}

// At a normal size the whole explanation is on screen, verbatim: this is the
// copy the H key exists to show.
func TestMetricsHelpShowsEveryCopyLine(t *testing.T) {
	a := newTestApp(120, 40)
	a.Update(keyPress("H"))

	out := stripANSI(a.View())
	for _, line := range metricsHelpCopyLines {
		if !strings.Contains(out, line) {
			t.Errorf("rendered overlay is missing copy line %q:\n%s", line, out)
		}
	}
}

// A short terminal scrolls instead of dropping the last definitions: the first
// screen shows the top, and the end key reaches the final line.
func TestMetricsHelpScrollsToTheLastLineOnAShortTerminal(t *testing.T) {
	a := newTestApp(100, 12)
	a.Update(keyPress("H"))

	first := stripANSI(a.View())
	if strings.Contains(first, "interpretable.") {
		t.Fatalf("the last line was already visible on a 12-row terminal:\n%s", first)
	}
	if !strings.Contains(first, "Window (w)") {
		t.Fatalf("the first screen does not start at the top:\n%s", first)
	}

	a.Update(keyPress("end"))
	last := stripANSI(a.View())
	if !strings.Contains(last, "interpretable.") {
		t.Fatalf("end did not reach the last copy line:\n%s", last)
	}
	if strings.Contains(last, "Window (w)") {
		t.Fatalf("the view did not scroll at all:\n%s", last)
	}
	if !strings.Contains(last, "H or esc") || !strings.Contains(last, "close") {
		t.Fatalf("the close hint is not pinned while scrolling:\n%s", last)
	}

	// pgup comes back, and the scroll clamps instead of running off either end.
	for i := 0; i < 30; i++ {
		a.Update(keyPress("pgup"))
	}
	if got := a.model.metricsHelpScroll; got != 0 {
		t.Errorf("scroll = %d after paging past the top, want 0", got)
	}
	for i := 0; i < 30; i++ {
		a.Update(keyPress("pgdown"))
	}
	out := stripANSI(a.View())
	if !strings.Contains(out, "interpretable.") {
		t.Errorf("paging past the bottom lost the last line:\n%s", out)
	}
	if got := a.model.metricsHelpScroll; got > len(metricsHelpBlocks)*3 {
		t.Errorf("scroll = %d ran past the content", got)
	}
}

// Very small terminals are where a fixed-height dialog panics or prints zero
// rows. It only has to stay usable: some text, no panic.
func TestMetricsHelpSurvivesTinyTerminals(t *testing.T) {
	for _, h := range []int{1, 2, 3, 5, 8, 11, 14} {
		for _, w := range []int{8, 16, 24, 40, 80} {
			a := newTestApp(w, h)
			a.model.metricsHelpActive = true
			out := stripANSI(a.renderMetricsHelp(""))
			if strings.TrimSpace(out) == "" {
				t.Errorf("%dx%d: overlay rendered nothing", w, h)
			}
		}
	}
}

// The footer advertises the overlay only where it exists, and it keeps saying so
// while the workflow step panel has focus.
func TestMetricsHelpFooterHintOnlyOnMetricsTabs(t *testing.T) {
	for _, tab := range NewModel().tabs {
		a := newTestApp(160, 40)
		a.model.SetActiveTab(tab)

		out := stripANSI(a.renderFooter())
		hasHint := strings.Contains(out, "H") && strings.Contains(out, "help")
		want := metricsHelpTab(tab)
		if hasHint != want {
			t.Errorf("%s: footer hint present = %v, want %v: %q", tab, hasHint, want, out)
		}
	}

	a := newTestApp(160, 40)
	a.model.SetActiveTab("Workflows")
	a.model.workflowsTab.Focus = WorkflowsViewSteps
	if out := stripANSI(a.renderFooter()); !strings.Contains(out, "help") {
		t.Errorf("Workflows step panel lost the help hint: %q", out)
	}
}
