package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newTestClient(t *testing.T) *Client {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	c, err := New(context.Background(), path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestWorkflowInstance_CRUD(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	inst := &WorkflowInstance{
		ID:         "wf_1",
		WorkflowID: "feature-development",
		CellID:     "PLANE-142",
		SourceID:   "main-plane",
		State:      InstanceStatePending,
	}
	if err := c.CreateWorkflowInstance(ctx, inst); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := c.GetWorkflowInstance(ctx, "wf_1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected instance, got nil")
	}
	if got.WorkflowID != "feature-development" || got.CellID != "PLANE-142" || got.State != InstanceStatePending {
		t.Errorf("instance fields wrong: %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Error("created_at not set")
	}

	if err := c.UpdateWorkflowInstanceState(ctx, "wf_1", InstanceStateRunning); err != nil {
		t.Fatalf("update state: %v", err)
	}
	got, _ = c.GetWorkflowInstance(ctx, "wf_1")
	if got.State != InstanceStateRunning {
		t.Errorf("state not updated: %s", got.State)
	}
}

func TestWorkflowInstance_NotFound(t *testing.T) {
	c := newTestClient(t)
	got, err := c.GetWorkflowInstance(context.Background(), "missing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing instance, got: %+v", got)
	}
}

func TestWorkflowInstance_ListByState(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	for i, st := range []string{InstanceStateApprovalWaiting, InstanceStateRunning, InstanceStateApprovalWaiting} {
		inst := &WorkflowInstance{
			ID:         "wf_" + string(rune('a'+i)),
			WorkflowID: "wf",
			CellID:     "c",
			State:      st,
			CreatedAt:  time.Unix(int64(1000+i), 0),
		}
		if err := c.CreateWorkflowInstance(ctx, inst); err != nil {
			t.Fatal(err)
		}
	}

	waiting, err := c.ListWorkflowInstancesByState(ctx, InstanceStateApprovalWaiting)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(waiting) != 2 {
		t.Fatalf("expected 2 approval_waiting, got %d", len(waiting))
	}
	// oldest first
	if !waiting[0].CreatedAt.Before(waiting[1].CreatedAt) {
		t.Error("expected oldest-first ordering")
	}
}

func TestWorkflowInstance_ReconcileOrphans(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	_ = c.CreateWorkflowInstance(ctx, &WorkflowInstance{ID: "r1", WorkflowID: "w", CellID: "c", State: InstanceStateRunning})
	_ = c.CreateWorkflowInstance(ctx, &WorkflowInstance{ID: "r2", WorkflowID: "w", CellID: "c", State: InstanceStateRunning})
	_ = c.CreateWorkflowInstance(ctx, &WorkflowInstance{ID: "w1", WorkflowID: "w", CellID: "c", State: InstanceStateApprovalWaiting})

	n, err := c.ReconcileOrphanWorkflowInstances(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 reconciled, got %d", n)
	}

	// approval_waiting is left untouched.
	w1, _ := c.GetWorkflowInstance(ctx, "w1")
	if w1.State != InstanceStateApprovalWaiting {
		t.Errorf("approval_waiting should be untouched, got %s", w1.State)
	}
	r1, _ := c.GetWorkflowInstance(ctx, "r1")
	if r1.State != InstanceStateInterrupted {
		t.Errorf("running should become interrupted, got %s", r1.State)
	}
}

func TestHasActiveInstanceForRoute(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	// task T1: triage already done; implementation running.
	_ = c.CreateWorkflowInstance(ctx, &WorkflowInstance{ID: "t1-triage", WorkflowID: "triage", CellID: "1948", TaskID: "T1", State: InstanceStateDone})
	_ = c.CreateWorkflowInstance(ctx, &WorkflowInstance{ID: "t1-impl", WorkflowID: "implementation", CellID: "1948", TaskID: "T1", State: InstanceStateRunning})
	// task T2: implementation parked at an approval step.
	_ = c.CreateWorkflowInstance(ctx, &WorkflowInstance{ID: "t2-impl", WorkflowID: "implementation", CellID: "2000", TaskID: "T2", State: InstanceStateApprovalWaiting})
	// task T3: implementation failed (terminal — eligible for retry, must NOT block).
	_ = c.CreateWorkflowInstance(ctx, &WorkflowInstance{ID: "t3-impl", WorkflowID: "implementation", CellID: "3000", TaskID: "T3", State: InstanceStateFailed})

	cases := []struct {
		name       string
		taskID     string
		workflowID string
		want       bool
	}{
		{"running blocks", "T1", "implementation", true},
		{"approval_waiting blocks (the park gap)", "T2", "implementation", true},
		{"done earlier workflow does not block hand-off", "T1", "triage", false},
		{"different task not blocked", "T2", "triage", false},
		{"failed is terminal, retry allowed", "T3", "implementation", false},
		{"unknown task", "T9", "implementation", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.HasActiveInstanceForRoute(ctx, tc.taskID, tc.workflowID)
			if err != nil {
				t.Fatalf("HasActiveInstanceForRoute: %v", err)
			}
			if got != tc.want {
				t.Errorf("HasActiveInstanceForRoute(%q,%q) = %v, want %v", tc.taskID, tc.workflowID, got, tc.want)
			}
		})
	}
}

func TestWorkflowInstance_ListByTask(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Two instances for task T1 (fan-out), one for T2.
	_ = c.CreateWorkflowInstance(ctx, &WorkflowInstance{ID: "i1", WorkflowID: "wf-a", CellID: "c1", TaskID: "T1", State: InstanceStateDone, CreatedAt: base})
	_ = c.CreateWorkflowInstance(ctx, &WorkflowInstance{ID: "i2", WorkflowID: "wf-b", CellID: "c1", TaskID: "T1", State: InstanceStateRunning, CreatedAt: base.Add(time.Minute)})
	_ = c.CreateWorkflowInstance(ctx, &WorkflowInstance{ID: "i3", WorkflowID: "wf-c", CellID: "c2", TaskID: "T2", State: InstanceStateDone, CreatedAt: base.Add(2 * time.Minute)})

	got, err := c.ListWorkflowInstancesByTask(ctx, "T1")
	if err != nil {
		t.Fatalf("ListWorkflowInstancesByTask: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d instances for T1, want 2", len(got))
	}
	// Newest first: i2 before i1.
	if got[0].ID != "i2" || got[1].ID != "i1" {
		t.Errorf("order = [%s %s], want newest-first [i2 i1]", got[0].ID, got[1].ID)
	}
	if got[0].TaskID != "T1" {
		t.Errorf("TaskID = %q, want T1", got[0].TaskID)
	}
	if none, _ := c.ListWorkflowInstancesByTask(ctx, "missing"); len(none) != 0 {
		t.Errorf("ListWorkflowInstancesByTask(unknown) = %d, want 0", len(none))
	}
}

func TestStepRun_CRUD(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	_ = c.CreateWorkflowInstance(ctx, &WorkflowInstance{ID: "wf_1", WorkflowID: "w", CellID: "c", State: InstanceStateRunning})

	started := time.Unix(2000, 0)
	sr := &StepRun{
		ID:                 "sr_1",
		WorkflowInstanceID: "wf_1",
		StepID:             "plan",
		AgentID:            "architect",
		State:              StepStateRunning,
		StartedAt:          &started,
	}
	if err := c.CreateStepRun(ctx, sr); err != nil {
		t.Fatalf("create step run: %v", err)
	}

	finished := time.Unix(2100, 0)
	sr.State = StepStatePassed
	sr.Output = "did the work"
	sr.StructuredOutput = `{"complexity":"high"}`
	sr.Summary = "- planned it"
	sr.FinishedAt = &finished
	if err := c.UpdateStepRun(ctx, sr); err != nil {
		t.Fatalf("update step run: %v", err)
	}

	runs, err := c.ListStepRuns(ctx, "wf_1")
	if err != nil {
		t.Fatalf("list step runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 step run, got %d", len(runs))
	}
	got := runs[0]
	if got.State != StepStatePassed || got.Output != "did the work" {
		t.Errorf("step run not updated: %+v", got)
	}
	if got.StructuredOutput != `{"complexity":"high"}` {
		t.Errorf("structured output wrong: %q", got.StructuredOutput)
	}
	if got.Summary != "- planned it" {
		t.Errorf("summary wrong: %q", got.Summary)
	}
	if got.FinishedAt == nil {
		t.Error("finished_at not persisted")
	}
}

func TestStepRun_OrderedByInsertion(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	_ = c.CreateWorkflowInstance(ctx, &WorkflowInstance{ID: "wf_1", WorkflowID: "w", CellID: "c", State: InstanceStateRunning})

	for _, id := range []string{"plan", "implement", "review"} {
		if err := c.CreateStepRun(ctx, &StepRun{ID: "sr-" + id, WorkflowInstanceID: "wf_1", StepID: id, State: StepStatePending}); err != nil {
			t.Fatal(err)
		}
	}
	runs, _ := c.ListStepRuns(ctx, "wf_1")
	if len(runs) != 3 || runs[0].StepID != "plan" || runs[2].StepID != "review" {
		t.Errorf("unexpected step run order: %+v", runs)
	}
}
