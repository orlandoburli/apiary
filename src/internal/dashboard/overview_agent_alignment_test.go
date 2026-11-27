package dashboard

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// overviewRowColumn returns the display column where runStr starts on the one
// Overview line that carries it. The counts are what must line up; where the
// id ends is what shifts them around. It measures in columns rather than bytes
// because the box border and the truncation ellipsis are both multi-byte.
func overviewRowColumn(t *testing.T, out, runStr string) int {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, runStr); i >= 0 {
			return lipgloss.Width(line[:i])
		}
	}
	t.Fatalf("no Overview row contains %q:\n%s", runStr, out)
	return -1
}

// The agent list used to pad every id to a fixed 16 columns, so one long id
// (workspace-du-qa-engineer) pushed its counts right while the rows above and
// below stayed left. The column now follows the longest id present.
func TestOverviewAgentCountsAlignWithLongID(t *testing.T) {
	const longID = "workspace-du-qa-engineer"

	a := newTestApp(120, 80)
	a.model.overviewTab = &OverviewTab{
		Status: "running",
		AgentBreakdown: []AgentCount{
			{ID: "coder", Running: 3, MaxWorkers: 7},
			{ID: longID, Running: 1, MaxWorkers: 9},
		},
	}

	out := stripANSI(a.renderOverviewTab(80))
	shortCol := overviewRowColumn(t, out, "3/7")
	longCol := overviewRowColumn(t, out, "1/9")

	if shortCol != longCol {
		t.Errorf("counts start at columns %d and %d, want the same column:\n%s", shortCol, longCol, out)
	}
	if want := 3 + len(longID); longCol != want {
		t.Errorf("counts start at column %d, want %d (border, indent and the longest id):\n%s", longCol, want, out)
	}
}

// One absurd id must not shove the counts off the row, so the column stops at
// the cap and the id is shortened to fit it.
func TestOverviewAgentCountsAlignWhenIDExceedsCap(t *testing.T) {
	longID := strings.Repeat("workspace-agent-", 3)

	a := newTestApp(120, 80)
	a.model.overviewTab = &OverviewTab{
		Status: "running",
		AgentBreakdown: []AgentCount{
			{ID: "coder", Running: 3, MaxWorkers: 7},
			{ID: longID, Running: 1, MaxWorkers: 9},
		},
	}

	out := stripANSI(a.renderOverviewTab(80))
	shortCol := overviewRowColumn(t, out, "3/7")
	longCol := overviewRowColumn(t, out, "1/9")

	if shortCol != longCol {
		t.Errorf("counts start at columns %d and %d, want the same column:\n%s", shortCol, longCol, out)
	}
	if want := 3 + overviewAgentIDMax; longCol != want {
		t.Errorf("counts start at column %d, want %d (border, indent and the cap):\n%s", longCol, want, out)
	}
	if !strings.Contains(out, truncate(longID, overviewAgentIDMax)) {
		t.Errorf("the over-long id was not truncated to %d columns:\n%s", overviewAgentIDMax, out)
	}
	if strings.Contains(out, longID) {
		t.Errorf("the over-long id was rendered in full:\n%s", out)
	}
}
