package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

func mustCreateInstance(t *testing.T, dbc *db.Client, inst *db.WorkflowInstance) {
	t.Helper()
	if err := dbc.CreateWorkflowInstance(context.Background(), inst); err != nil {
		t.Fatalf("create instance %s: %v", inst.ID, err)
	}
}

// TestTaskHistory_OrdersInstancesAndMapsStepsAndLogs verifies the IPC mapping:
// segments are oldest-first and each instance's summary/steps/logs are carried
// into the view. (Precise per-instance log windowing is covered at the db layer
// in TestGetTaskWorkflowHistory_*.)
func TestTaskHistory_OrdersInstancesAndMapsStepsAndLogs(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	const task, cell = "task-1", "issue-1948"
	// Past dates so the WriteTaskLog line (timestamped now) lands in the latest,
	// open-ended instance window regardless of when the test runs.
	t0 := time.Date(2020, 1, 1, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(10 * time.Minute)

	mustCreateInstance(t, dbc, &db.WorkflowInstance{ID: "wi_inv", WorkflowID: "investigator", TaskID: task, CellID: cell, State: db.InstanceStateDone, CreatedAt: t0})
	mustCreateInstance(t, dbc, &db.WorkflowInstance{ID: "wi_impl", WorkflowID: "implementation", TaskID: task, CellID: cell, State: db.InstanceStateRunning, CreatedAt: t1})

	for _, s := range []struct{ inst, step string }{{"wi_inv", "classify"}, {"wi_inv", "triage"}, {"wi_impl", "implement"}} {
		if err := dbc.CreateStepRun(ctx, &db.StepRun{ID: s.inst + "-" + s.step, WorkflowInstanceID: s.inst, StepID: s.step, AgentID: "ag", State: db.StepStatePassed}); err != nil {
			t.Fatalf("create step %s/%s: %v", s.inst, s.step, err)
		}
	}
	_ = dbc.WriteTaskLog(ctx, cell, "INFO", "started implementation workflow")

	resp, err := (&Dispatcher{db: dbc}).TaskHistory(ctx, task)
	if err != nil {
		t.Fatalf("TaskHistory: %v", err)
	}
	if resp == nil || len(resp.Segments) != 2 {
		t.Fatalf("expected 2 segments, got %+v", resp)
	}
	if resp.Segments[0].Instance.Workflow != "investigator" || resp.Segments[1].Instance.Workflow != "implementation" {
		t.Fatalf("segments out of order: %q then %q (want investigator then implementation)",
			resp.Segments[0].Instance.Workflow, resp.Segments[1].Instance.Workflow)
	}
	if len(resp.Segments[0].Steps) != 2 {
		t.Errorf("investigator step views = %d, want 2", len(resp.Segments[0].Steps))
	}
	if len(resp.Segments[1].Steps) != 1 || resp.Segments[1].Steps[0].StepID != "implement" {
		t.Errorf("implementation steps = %+v, want [implement]", resp.Segments[1].Steps)
	}
	if resp.Segments[1].Instance.State != db.InstanceStateRunning {
		t.Errorf("implementation state = %q, want running", resp.Segments[1].Instance.State)
	}
	if len(resp.Segments[1].Logs) != 1 || resp.Segments[1].Logs[0].Message != "started implementation workflow" {
		t.Errorf("implementation logs = %+v, want one mapped line", resp.Segments[1].Logs)
	}
}

func TestTaskHistory_NoInstancesReturnsNil(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	resp, err := (&Dispatcher{db: dbc}).TaskHistory(ctx, "nobody")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Errorf("expected nil for a task with no instances, got %+v", resp)
	}
}

// TestResolveHistoryTaskID_AcceptsHumanReference is the #471 regression case:
// /tasks/history's ?source=&item= used to match only the exact source_item_id
// (GetBindingBySourceItem straight-up), so a Jira key — the only form of the
// reference that ever appears in Jira's UI — resolved to nothing and the CLI
// reported "Daemon is not running" for a daemon that was up. The item must
// resolve through the same vocabulary dispatch/restart use (resolveCellRef).
func TestResolveHistoryTaskID_AcceptsHumanReference(t *testing.T) {
	ctx := context.Background()
	dbc := openTestDB(ctx, t)
	taskID := seedJiraTask(ctx, t, dbc, "300966", "PSP-278")

	d := &Dispatcher{db: dbc}

	got, err := d.resolveHistoryTaskID(ctx, "jira", "PSP-278")
	if err != nil {
		t.Fatalf("resolveHistoryTaskID(PSP-278): %v", err)
	}
	if got != taskID {
		t.Errorf("resolved task = %q, want %q", got, taskID)
	}

	// The cell id form (what the old code required) must keep working too.
	got, err = d.resolveHistoryTaskID(ctx, "jira", "300966")
	if err != nil {
		t.Fatalf("resolveHistoryTaskID(300966): %v", err)
	}
	if got != taskID {
		t.Errorf("resolved task by cell id = %q, want %q", got, taskID)
	}
}

// TestResolveHistoryTaskID_UnresolvedReferenceIsNotFound covers the second half
// of #471: an item that does not resolve — wrong key, wrong source, or a bound
// item with no history yet — must fail with a message naming the reference, not
// a generic error the CLI could mistake for the daemon being unreachable.
func TestResolveHistoryTaskID_UnresolvedReferenceIsNotFound(t *testing.T) {
	ctx := context.Background()
	dbc := openTestDB(ctx, t)
	seedJiraTask(ctx, t, dbc, "300966", "PSP-278")

	d := &Dispatcher{db: dbc}

	_, err := d.resolveHistoryTaskID(ctx, "jira", "PSP-999")
	if err == nil {
		t.Fatal("expected an error for an unbound reference, got nil")
	}
	if !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("error = %v, want it to wrap ErrTaskNotFound", err)
	}
	if !strings.Contains(err.Error(), "PSP-999") || !strings.Contains(err.Error(), "jira") {
		t.Errorf("error %q should name the unresolved reference and source", err)
	}
}

// TestResolveHistoryTaskID_AmbiguousReferenceIsRejected: a reference that matches
// items in two different sources must not be guessed at (same #377 guard restart
// uses), and must surface as ErrAmbiguousRef rather than a plain not-found. It
// only triggers when source is left unscoped (?source= empty), since a caller
// who names the source is asking to be scoped to it.
func TestResolveHistoryTaskID_AmbiguousReferenceIsRejected(t *testing.T) {
	ctx := context.Background()
	dbc := openTestDB(ctx, t)

	for _, s := range []struct{ source, itemID string }{{"jira", "10042"}, {"github", "77"}} {
		task := &model.InternalTask{Title: "dup", State: model.TaskStateRunning}
		binding := &model.SourceBinding{SourceID: s.source, SourceItemID: s.itemID, SourceItemNumber: "DUP-1"}
		if err := dbc.CreateTaskWithBinding(ctx, task, binding); err != nil {
			t.Fatalf("seed %s: %v", s.source, err)
		}
	}

	d := &Dispatcher{db: dbc}
	_, err := d.resolveHistoryTaskID(ctx, "", "DUP-1")
	if !errors.Is(err, ErrAmbiguousRef) {
		t.Fatalf("error = %v, want it to wrap ErrAmbiguousRef", err)
	}
}
