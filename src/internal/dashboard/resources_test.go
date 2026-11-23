package dashboard

import (
	"errors"
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/procstat"
)

// fakeUsage is a snapshot with the daemon at pid 100 and one agent at pid 200
// with a node child under it, so the tree walk has something to walk.
func fakeUsage(baseline bool) *procstat.Usage {
	return &procstat.Usage{
		Baseline: baseline,
		Processes: []procstat.Process{
			{PID: 100, PPID: 1, RSS: 80 << 20, CPUPercent: 1.5},
			{PID: 200, PPID: 100, RSS: 20 << 20, CPUPercent: 40},
			{PID: 201, PPID: 200, RSS: 5 << 20, CPUPercent: 10},
		},
	}
}

// An agent's cost is its whole tree: the CLI plus the MCP servers and node
// children under it, which is where most of the memory actually goes.
func TestRunningResourceCellsShowTheWholeTree(t *testing.T) {
	a := newTestApp(140, 40)
	a.resourceUsage = fakeUsage(false)

	cpu, mem := a.runningResourceCells(200)
	if cpu != "50.0%" {
		t.Errorf("cpu = %q, want 50.0%% (the agent plus its node child)", cpu)
	}
	if mem != "25.0 MiB" {
		t.Errorf("mem = %q, want 25.0 MiB", mem)
	}
}

// The first sample cannot produce a CPU percentage — there is no earlier
// reading to difference against — so it must say so rather than claim 0%.
func TestRunningResourceCellsHideCPUOnTheBaselineSample(t *testing.T) {
	a := newTestApp(140, 40)
	a.resourceUsage = fakeUsage(true)

	if cpu, _ := a.runningResourceCells(200); cpu != "—" {
		t.Errorf("cpu on the baseline sample = %q, want an em dash", cpu)
	}
	if _, mem := a.runningResourceCells(200); mem == "—" || mem == "" {
		t.Errorf("mem on the baseline sample = %q, want a real figure", mem)
	}
}

// A pid with no measurement shows an em dash: the process may have exited
// between the database query and the sample, and 0 MiB would be a lie.
func TestRunningResourceCellsForAMissingProcess(t *testing.T) {
	a := newTestApp(140, 40)
	a.resourceUsage = fakeUsage(false)

	if cpu, mem := a.runningResourceCells(999); cpu != "—" || mem != "—" {
		t.Errorf("missing pid = %q/%q, want em dashes", cpu, mem)
	}
	if cpu, mem := a.runningResourceCells(0); cpu != "—" || mem != "—" {
		t.Errorf("pid 0 = %q/%q, want em dashes", cpu, mem)
	}
}

// The Overview reports the hive's tree and the daemon's own process, so the
// reader can tell how much of the total is the daemon itself.
func TestOverviewResourcesReportTheTreeAndTheDaemon(t *testing.T) {
	a := newTestApp(140, 40)
	a.resourceUsage = fakeUsage(false)
	a.daemonPID = 100

	out := stripANSI(a.renderOverviewResources())
	for _, want := range []string{"Hive:", "Daemon:", "51.5%", "105.0 MiB", "3 processes"} {
		if !strings.Contains(out, want) {
			t.Errorf("resource section is missing %q:\n%s", want, out)
		}
	}
}

// With no daemon pid there is no tree to measure, and the section says that
// instead of printing a zero.
func TestOverviewResourcesSayWhenTheDaemonIsUnreachable(t *testing.T) {
	a := newTestApp(140, 40)
	a.resourceUsage = fakeUsage(false)
	a.daemonPID = 0

	out := stripANSI(a.renderOverviewResources())
	if !strings.Contains(out, "not reachable") {
		t.Errorf("resource section = %q, want it to say the daemon is unreachable", out)
	}
}

// The Overview tab renders the section, not just the helper.
func TestOverviewTabIncludesTheResourceSection(t *testing.T) {
	a := newTestApp(140, 40)
	a.model.overviewTab = &OverviewTab{Status: "running"}
	a.resourceUsage = fakeUsage(false)
	a.daemonPID = 100

	out := stripANSI(a.renderOverviewTab(60))
	if !strings.Contains(out, "Resources") {
		t.Errorf("the Overview tab has no resource section:\n%s", out)
	}
}

// A sample that lands in the model is what the render reads. A failed sample
// must leave the previous snapshot in place rather than blanking the panel.
func TestResourceSnapshotIsStoredAndSurvivesAFailedSample(t *testing.T) {
	a := newTestApp(140, 40)
	a.resourceUsage = fakeUsage(false)
	a.daemonPID = 100

	a.Update(resourceDataMsg{err: errors.New("sample failed")})
	if a.resourceUsage == nil {
		t.Error("a failed sample cleared the previous snapshot")
	}

	fresh := fakeUsage(true)
	a.Update(resourceDataMsg{usage: fresh, daemonPID: 100})
	if a.resourceUsage != fresh {
		t.Error("a successful sample did not replace the snapshot")
	}
}
