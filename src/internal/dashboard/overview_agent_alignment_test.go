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

// assertSpaceBeforeRun checks that the count on the row carrying runStr is
// separated from the id. An id that exactly fills the padded column used to
// butt straight against its count ("qa-mobile-explorer0/1"), which lines the
// columns up perfectly while making the row unreadable.
func assertSpaceBeforeRun(t *testing.T, out, runStr string) {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		i := strings.Index(line, runStr)
		if i < 0 {
			continue
		}
		if i == 0 || !strings.HasSuffix(line[:i], " ") {
			t.Errorf("id and count %q have no separating space in row %q", runStr, line)
		}
		return
	}
	t.Fatalf("no Overview row contains %q:\n%s", runStr, out)
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
	if want := 3 + len(longID) + 1; longCol != want {
		t.Errorf("counts start at column %d, want %d (border, indent, the longest id and the separator):\n%s", longCol, want, out)
	}
	assertSpaceBeforeRun(t, out, "3/7")
	assertSpaceBeforeRun(t, out, "1/9")
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
	if want := 3 + overviewAgentIDMax + 1; longCol != want {
		t.Errorf("counts start at column %d, want %d (border, indent, the cap and the separator):\n%s", longCol, want, out)
	}
	assertSpaceBeforeRun(t, out, "3/7")
	assertSpaceBeforeRun(t, out, "1/9")
	if !strings.Contains(out, truncate(longID, overviewAgentIDMax)) {
		t.Errorf("the over-long id was not truncated to %d columns:\n%s", overviewAgentIDMax, out)
	}
	if strings.Contains(out, longID) {
		t.Errorf("the over-long id was rendered in full:\n%s", out)
	}
}
