package dashboard

import (
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/state"
)

func progressWorkflows() []config.WorkflowConfig {
	return []config.WorkflowConfig{{
		ID: "wf",
		Steps: []config.StepConfig{
			{ID: "plan"}, {ID: "build"}, {ID: "review"}, {ID: "ship"}, {ID: "verify"},
		},
	}}
}

func TestResolveTaskProgress(t *testing.T) {
	rows := []db.TaskStepRow{
		// One live instance, third step running → "review 3/5".
		{TaskID: "T1", InstanceID: "i1", WorkflowID: "wf", StepID: "plan", StepState: "done"},
		{TaskID: "T1", InstanceID: "i1", WorkflowID: "wf", StepID: "build", StepState: "done"},
		{TaskID: "T1", InstanceID: "i1", WorkflowID: "wf", StepID: "review", StepState: "running"},

		// Two live instances → fan-out, no single current step.
		{TaskID: "T2", InstanceID: "i2", WorkflowID: "wf", StepID: "plan", StepState: "running"},
		{TaskID: "T2", InstanceID: "i3", WorkflowID: "wf", StepID: "build", StepState: "running"},

		// Dispatched but no step row yet.
		{TaskID: "T3", InstanceID: "i4", WorkflowID: "wf", StepID: "", StepState: ""},

		// Every step settled: fall back to the most recent one.
		{TaskID: "T4", InstanceID: "i5", WorkflowID: "wf", StepID: "plan", StepState: "done"},
		{TaskID: "T4", InstanceID: "i5", WorkflowID: "wf", StepID: "build", StepState: "done"},
	}

	got := resolveTaskProgress(rows, progressWorkflows())

	if p := got["T1"]; p.StepID != "review" || p.Position != 3 || p.Total != 5 || p.Instances != 1 {
		t.Errorf("T1 = %+v, want review 3/5 with 1 instance", p)
	}
	if p := got["T2"]; p.Instances != 2 || p.StepID != "" {
		t.Errorf("T2 = %+v, want a fan-out marker and no single step", p)
	}
	if p := got["T3"]; p.Instances != 1 || p.StepID != "" {
		t.Errorf("T3 = %+v, want one instance and no step yet", p)
	}
	if p := got["T4"]; p.StepID != "build" || p.Position != 2 {
		t.Errorf("T4 = %+v, want the most recent step when none is running", p)
	}
}

// TestResolveTaskProgress_BlockedStepIsCurrent pins that a parked step is the
// task's current position. A step waiting on an approval is where the task is,
// and reporting the previous step instead would point the operator at work that
// is already finished.
func TestResolveTaskProgress_BlockedStepIsCurrent(t *testing.T) {
	rows := []db.TaskStepRow{
		{TaskID: "T1", InstanceID: "i1", WorkflowID: "wf", StepID: "plan", StepState: "done"},
		{TaskID: "T1", InstanceID: "i1", WorkflowID: "wf", StepID: "build", StepState: "blocked",
			BlockedReason: string(state.ReasonApproval)},
	}
	if p := resolveTaskProgress(rows, progressWorkflows())["T1"]; p.StepID != "build" {
		t.Errorf("progress = %+v, want the blocked step to be current", p)
	}
}

func TestProgressLabel(t *testing.T) {
	cases := []struct {
		name  string
		p     taskProgress
		width int
		want  string
	}{
		{"running step", taskProgress{StepID: "review", Position: 3, Total: 5, Instances: 1}, 16, "review 3/5"},
		{"fan-out", taskProgress{Instances: 3}, 16, "⑂ 3 steps"},
		{"no instance", taskProgress{}, 16, "—"},
		{"unknown workflow", taskProgress{StepID: "review", Instances: 1}, 16, "review"},
		{"edited workflow", taskProgress{StepID: "gone", Total: 5, Instances: 1}, 16, "gone ?/5"},
	}
	for _, c := range cases {
		if got := progressLabel(c.p, c.width); got != c.want {
			t.Errorf("%s: progressLabel = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestProgressLabel_TruncatesNameNotPosition pins the truncation rule: the
// position is the part a reader cannot infer, so a long step id loses its tail
// rather than its fraction.
func TestProgressLabel_TruncatesNameNotPosition(t *testing.T) {
	p := taskProgress{StepID: "implement-the-whole-feature", Position: 3, Total: 5, Instances: 1}
	got := progressLabel(p, 16)

	if !strings.HasSuffix(got, " 3/5") {
		t.Errorf("progressLabel = %q, want it to keep the 3/5 suffix", got)
	}
	if len([]rune(got)) > 16 {
		t.Errorf("progressLabel = %q (%d runes), want it within 16", got, len([]rune(got)))
	}
}

// TestProgressCell_IsFixedWidth keeps the row aligned: the column replaced one
// of a fixed width, and every value must occupy exactly that.
func TestProgressCell_IsFixedWidth(t *testing.T) {
	for _, p := range []taskProgress{
		{StepID: "review", Position: 3, Total: 5, Instances: 1},
		{Instances: 4},
		{},
		{StepID: "a-very-long-step-identifier", Position: 1, Total: 9, Instances: 1},
	} {
		if w := len([]rune(progressCell(p, 16))); w != 16 {
			t.Errorf("progressCell(%+v) width = %d, want 16", p, w)
		}
	}
}
