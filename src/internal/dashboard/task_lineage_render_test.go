package dashboard

import (
	"strings"
	"testing"
	"time"
)

func TestRenderSourceBindings(t *testing.T) {
	d := &TaskItem{
		Bindings: []SourceBindingItem{
			{SourceID: "github", ItemNumber: "#42", ItemURL: "https://github.com/o/r/issues/42", ItemID: "42"},
			{SourceID: "plane", ItemNumber: "ERP-7", ItemURL: "https://plane.example/erp-7", ItemID: "erp7"},
		},
	}
	out := stripANSI(renderSourceBindings(d))
	for _, want := range []string{"Source Bindings", "#42", "github", "https://github.com/o/r/issues/42", "ERP-7", "plane"} {
		if !strings.Contains(out, want) {
			t.Errorf("renderSourceBindings missing %q:\n%s", want, out)
		}
	}
}

func TestRenderSourceBindings_Empty(t *testing.T) {
	if out := renderSourceBindings(&TaskItem{}); out != "" {
		t.Errorf("expected no output for a task with no bindings, got: %q", out)
	}
}

func TestRenderTaskLineage_Breadcrumb(t *testing.T) {
	d := &TaskItem{
		Lineage: []TaskLineageItem{
			{TaskID: "r", Title: "Incident", State: "done"},
			{TaskID: "m", Title: "Collect", State: "done"},
			{TaskID: "s", Title: "Staff", State: "running"},
		},
	}
	out := stripANSI(renderTaskLineage(d))
	for _, want := range []string{"Lineage", "Incident", "Collect", "Staff", " > "} {
		if !strings.Contains(out, want) {
			t.Errorf("renderTaskLineage breadcrumb missing %q:\n%s", want, out)
		}
	}
}

func TestRenderTaskLineage_ChildrenTree(t *testing.T) {
	d := &TaskItem{
		Children: []TaskLineageItem{
			{TaskID: "a", Title: "Fix A", State: "done", HasBinding: true, InstanceCount: 2},
			{TaskID: "b", Title: "Fix B", State: "registered", HasBinding: false, InstanceCount: 0},
		},
	}
	out := stripANSI(renderTaskLineage(d))
	for _, want := range []string{"Children (2)", "Fix A", "Fix B", "(2 inst)", "(0 inst)", "●"} {
		if !strings.Contains(out, want) {
			t.Errorf("renderTaskLineage children missing %q:\n%s", want, out)
		}
	}
	// The registered child's badge uses the short "queued" label.
	if !strings.Contains(out, "queued") {
		t.Errorf("expected registered state to render as 'queued':\n%s", out)
	}
}

func TestRenderTaskLineage_RootNoChildren(t *testing.T) {
	// A root task (single-element lineage) with no children renders nothing.
	d := &TaskItem{Lineage: []TaskLineageItem{{TaskID: "r", Title: "Root", State: "running"}}}
	if out := renderTaskLineage(d); out != "" {
		t.Errorf("expected no lineage output for a childless root, got: %q", out)
	}
}

func TestRenderTaskInstances(t *testing.T) {
	base := time.Date(2026, 6, 6, 10, 30, 0, 0, time.UTC)
	d := &TaskItem{
		Instances: []WorkflowInstanceItem{
			{ID: "i1", Workflow: "code-review", State: "done", CreatedAt: base},
			{ID: "i2", Workflow: "docs-update", State: "running", CreatedAt: base.Add(time.Minute), ResumedFrom: "i0"},
			{ID: "i3", Workflow: "sub-collect", State: "done", CreatedAt: base.Add(2 * time.Minute), ParentInstanceID: "i1"},
		},
	}
	out := stripANSI(renderTaskInstances(d))
	for _, want := range []string{"Workflow Instances (3)", "code-review", "docs-update", "sub-collect", "(resumed)", "(sub)"} {
		if !strings.Contains(out, want) {
			t.Errorf("renderTaskInstances missing %q:\n%s", want, out)
		}
	}
}

func TestRenderTaskInstances_Empty(t *testing.T) {
	if out := renderTaskInstances(&TaskItem{}); out != "" {
		t.Errorf("expected no output for a task with no instances, got: %q", out)
	}
}

// TestTaskDetailShowsTaskModelSections wires bindings, lineage, children, and
// instances into the full detail view and asserts they render inside a correctly
// framed box (9.1.2-9.1.4 + 9.2 in the Detail panel).
func TestTaskDetailShowsTaskModelSections(t *testing.T) {
	now := time.Now()
	a := newTestApp(90, 44)
	a.model.tasksTab.View = TaskViewDetail
	a.model.tasksTab.Detail = &TaskItem{
		TaskID:               "ISSUE-9",
		InternalTaskID:       "tk_child",
		Title:                "Collect incident logs",
		Status:               "running",
		OutstandingWorkflows: 2,
		ParentTitle:          "Payments incident",
		StartedAt:            &now,
		Bindings: []SourceBindingItem{
			{SourceID: "github", ItemNumber: "#9", ItemURL: "https://github.com/o/r/issues/9", ItemID: "9"},
		},
		Lineage: []TaskLineageItem{
			{TaskID: "tk_root", Title: "Payments incident", State: "running"},
			{TaskID: "tk_child", Title: "Collect incident logs", State: "running"},
		},
		Children: []TaskLineageItem{
			{TaskID: "tk_g", Title: "Staff", State: "registered", InstanceCount: 1},
		},
		Instances: []WorkflowInstanceItem{
			{ID: "i1", Workflow: "collect-logs", State: "running", CreatedAt: now},
		},
	}

	out := stripANSI(a.renderTaskDetail(a.model.tasksTab, 40))
	for _, want := range []string{
		"Outstanding", "Parent", "Payments incident",
		"Source Bindings", "#9", "github",
		"Lineage", "Children (1)", "Staff",
		"Workflow Instances (1)", "collect-logs",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("task detail missing %q:\n%s", want, out)
		}
	}
	assertFramed(t, a.renderTaskDetail(a.model.tasksTab, 40), 90)
}

// TestTaskStatusBadge_InternalStates verifies the badge handles the InternalTask
// lifecycle states (Phase 9) in addition to the legacy execution statuses, and
// stays within its 8-char column.
func TestTaskStatusBadge_InternalStates(t *testing.T) {
	cases := map[string]string{
		"registered":       "queued",
		"approval_waiting": "approval",
		"done":             "done",
		"running":          "running",
		"failed":           "failed",
		"success":          "done", // legacy execution status, now canonical
	}
	for state, want := range cases {
		out := stripANSI(taskStatusBadge(state))
		if !strings.Contains(out, want) {
			t.Errorf("taskStatusBadge(%q) = %q, want it to contain %q", state, out, want)
		}
		if w := len([]rune(strings.TrimRight(out, " "))); w > 8 {
			t.Errorf("taskStatusBadge(%q) label %q exceeds 8 chars", state, out)
		}
	}
}

// TestTaskStatusBadge_AllLayerStates covers the states the badge previously did
// not handle: workflow instance states, step states, and queue job states all
// reach this badge, and before internal/state they fell through to a muted
// badge showing the raw value — including "interrupted" (11 chars) and
// "skipped_cached" (14), which overflowed the 8-char column and pushed the rest
// of the row out of alignment.
func TestTaskStatusBadge_AllLayerStates(t *testing.T) {
	cases := map[string]string{
		// Workflow instance states.
		"pending":     "queued",
		"waiting":     "waiting",
		"interrupted": "halted",
		// Step states.
		"passed":         "done",
		"skipped":        "skipped",
		"skipped_cached": "skipped",
		// Queue job states.
		"leased":    "running",
		"succeeded": "done",
		"canceled":  "canceled",
		// Canonical states, for the release that starts writing them.
		"queued":  "queued",
		"blocked": "blocked",
	}
	for st, want := range cases {
		out := stripANSI(taskStatusBadge(st))
		if !strings.Contains(out, want) {
			t.Errorf("taskStatusBadge(%q) = %q, want it to contain %q", st, out, want)
		}
		if w := len([]rune(strings.TrimRight(out, " "))); w > 8 {
			t.Errorf("taskStatusBadge(%q) label %q exceeds 8 chars", st, out)
		}
	}
}

// TestTaskStatusBadge_UnknownIsTruncated pins that an unrecognised state is
// shown as stored rather than guessed at, but still cannot break the column.
func TestTaskStatusBadge_UnknownIsTruncated(t *testing.T) {
	out := stripANSI(taskStatusBadge("some_future_state"))
	if w := len([]rune(strings.TrimRight(out, " "))); w > 8 {
		t.Errorf("unknown state rendered %q, exceeding the 8-char column", out)
	}
}
