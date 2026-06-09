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

func TestCIPollChecks_RecordAndList(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	inst := &WorkflowInstance{ID: "wf_ci", WorkflowID: "implementation", CellID: "42", State: InstanceStateWaiting}
	if err := c.CreateWorkflowInstance(ctx, inst); err != nil {
		t.Fatalf("create instance: %v", err)
	}

	polls := []CIPollCheck{
		{WorkflowInstanceID: "wf_ci", StepID: "check-ci", Status: "pending", PRURL: "https://x/pr/1"},
		{WorkflowInstanceID: "wf_ci", StepID: "check-ci", Status: "pending", PRURL: "https://x/pr/1"},
		{WorkflowInstanceID: "wf_ci", StepID: "check-ci", Status: "failed", PRURL: "https://x/pr/1", Detail: `{"build":"failure"}`},
		{WorkflowInstanceID: "wf_ci", StepID: "check-ci", Status: "passed", PRURL: "https://x/pr/1"},
	}
	for i := range polls {
		if err := c.RecordCIPollCheck(ctx, &polls[i]); err != nil {
			t.Fatalf("record poll %d: %v", i, err)
		}
	}

	got, err := c.ListCIPollChecks(ctx, "wf_ci")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d polls, want 4", len(got))
	}
	// Oldest-first ordering and round-tripped fields.
	if got[0].Status != "pending" || got[3].Status != "passed" {
		t.Errorf("ordering wrong: %q … %q", got[0].Status, got[3].Status)
	}
	if got[2].Status != "failed" || got[2].Detail != `{"build":"failure"}` {
		t.Errorf("detail not round-tripped: %+v", got[2])
	}
	if got[0].PRURL != "https://x/pr/1" || got[0].CheckedAt.IsZero() {
		t.Errorf("pr_url/checked_at not populated: %+v", got[0])
	}

	// Isolated per instance.
	if other, _ := c.ListCIPollChecks(ctx, "wf_none"); len(other) != 0 {
		t.Errorf("expected no polls for unknown instance, got %d", len(other))
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

func TestHasCompletedInstanceForRoute(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	// task T1: decompose done; T2: decompose still running; T3: decompose failed.
	_ = c.CreateWorkflowInstance(ctx, &WorkflowInstance{ID: "t1-dec", WorkflowID: "decompose", CellID: "1986", TaskID: "T1", State: InstanceStateDone})
	_ = c.CreateWorkflowInstance(ctx, &WorkflowInstance{ID: "t2-dec", WorkflowID: "decompose", CellID: "1987", TaskID: "T2", State: InstanceStateRunning})
	_ = c.CreateWorkflowInstance(ctx, &WorkflowInstance{ID: "t3-dec", WorkflowID: "decompose", CellID: "1988", TaskID: "T3", State: InstanceStateFailed})

	cases := []struct {
		name       string
		taskID     string
		workflowID string
		want       bool
	}{
		{"done blocks re-dispatch", "T1", "decompose", true},
		{"running is not yet complete", "T2", "decompose", false},
		{"failed does not count as completed", "T3", "decompose", false},
		{"different workflow on same task", "T1", "implementation", false},
		{"unknown task", "T9", "decompose", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.HasCompletedInstanceForRoute(ctx, tc.taskID, tc.workflowID)
			if err != nil {
				t.Fatalf("HasCompletedInstanceForRoute: %v", err)
			}
			if got != tc.want {
				t.Errorf("HasCompletedInstanceForRoute(%q,%q) = %v, want %v", tc.taskID, tc.workflowID, got, tc.want)
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
	sr.InputPrompt = "you are an architect; plan it"
	sr.InputTokens = 120
	sr.OutputTokens = 80
	sr.TotalTokens = 200
	sr.CacheCreationTokens = 60
	sr.CacheReadTokens = 40
	sr.NumTurns = 3
	sr.NumToolCalls = 5
	sr.CostUSD = 0.0123
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
	if got.InputPrompt != "you are an architect; plan it" {
		t.Errorf("input prompt wrong: %q", got.InputPrompt)
	}
	if got.InputTokens != 120 || got.OutputTokens != 80 || got.TotalTokens != 200 {
		t.Errorf("token columns wrong: %+v", got)
	}
	if got.CacheCreationTokens != 60 || got.CacheReadTokens != 40 {
		t.Errorf("cache token columns wrong: %+v", got)
	}
	if got.NumTurns != 3 || got.NumToolCalls != 5 {
		t.Errorf("turn/tool-call columns wrong: %+v", got)
	}
	if got.CostUSD != 0.0123 {
		t.Errorf("cost wrong: %v", got.CostUSD)
	}
	if !StepRunHasUsage(got) {
		t.Error("StepRunHasUsage should be true for a row with tokens/cost")
	}
	if StepRunHasUsage(StepRun{}) {
		t.Error("StepRunHasUsage should be false for an empty row")
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

func TestReconcileOrphanWorkflowInstances_Extended(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	// Create instances in various states: running (orphan), approval_waiting (rehydrated),
	// pending, done, failed (terminal states not reconciled).
	now := time.Now()
	instances := []*WorkflowInstance{
		{ID: "wf_running", WorkflowID: "w", CellID: "c", State: InstanceStateRunning, CreatedAt: now},
		{ID: "wf_approval", WorkflowID: "w", CellID: "c", State: InstanceStateApprovalWaiting, CreatedAt: now},
		{ID: "wf_done", WorkflowID: "w", CellID: "c", State: InstanceStateDone, CreatedAt: now},
		{ID: "wf_failed", WorkflowID: "w", CellID: "c", State: InstanceStateFailed, CreatedAt: now},
	}
	for _, inst := range instances {
		if err := c.CreateWorkflowInstance(ctx, inst); err != nil {
			t.Fatalf("create instance: %v", err)
		}
	}

	// Reconcile orphaned instances.
	n, err := c.ReconcileOrphanWorkflowInstances(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// Should reconcile only 1 instance (running); approval_waiting is rehydrated separately.
	if n != 1 {
		t.Fatalf("expected 1 reconciled, got %d", n)
	}

	// Verify only running was changed to interrupted, others are untouched.
	for id, expectedState := range map[string]string{
		"wf_running":  InstanceStateInterrupted,     // running → interrupted
		"wf_approval": InstanceStateApprovalWaiting, // approval_waiting (untouched, rehydrated separately)
		"wf_done":     InstanceStateDone,            // done (unchanged)
		"wf_failed":   InstanceStateFailed,          // failed (unchanged)
	} {
		inst, err := c.GetWorkflowInstance(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if inst.State != expectedState {
			t.Errorf("%s: expected state %q, got %q", id, expectedState, inst.State)
		}
	}
}
