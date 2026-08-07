package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
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

func TestHasResumeDescendant(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	_ = c.CreateWorkflowInstance(ctx, &WorkflowInstance{ID: "a", WorkflowID: "w", CellID: "c", State: InstanceStateInterrupted})
	_ = c.CreateWorkflowInstance(ctx, &WorkflowInstance{ID: "b", WorkflowID: "w", CellID: "c", State: InstanceStateInterrupted})
	_ = c.CreateWorkflowInstance(ctx, &WorkflowInstance{ID: "a2", WorkflowID: "w", CellID: "c", State: InstanceStateRunning, ResumedFrom: "a"})

	if got, err := c.HasResumeDescendant(ctx, "a"); err != nil || !got {
		t.Errorf("HasResumeDescendant(a) = %v, %v; want true", got, err)
	}
	if got, err := c.HasResumeDescendant(ctx, "b"); err != nil || got {
		t.Errorf("HasResumeDescendant(b) = %v, %v; want false", got, err)
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

func TestReconcileOrphanStepRuns(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	// One interrupted instance (an orphan reconciled at startup) and one done
	// instance (a genuinely finished run). Only step_runs under the interrupted
	// instance should be touched.
	instances := []*WorkflowInstance{
		{ID: "wf_interrupted", WorkflowID: "w", CellID: "c", State: InstanceStateInterrupted},
		{ID: "wf_done", WorkflowID: "w", CellID: "c", State: InstanceStateDone},
	}
	for _, inst := range instances {
		if err := c.CreateWorkflowInstance(ctx, inst); err != nil {
			t.Fatalf("create instance: %v", err)
		}
	}

	// Step runs in a mix of states across both instances.
	steps := []*StepRun{
		// Under the interrupted parent: the two non-terminal ones are orphans.
		{ID: "sr_run", WorkflowInstanceID: "wf_interrupted", StepID: "implement", State: StepStateRunning},
		{ID: "sr_pend", WorkflowInstanceID: "wf_interrupted", StepID: "review", State: StepStatePending},
		{ID: "sr_pass", WorkflowInstanceID: "wf_interrupted", StepID: "classify", State: StepStatePassed},
		// Under the done parent: a leftover 'running' here must NOT be touched,
		// since its parent was not reconciled to interrupted.
		{ID: "sr_done_run", WorkflowInstanceID: "wf_done", StepID: "implement", State: StepStateRunning},
	}
	for _, sr := range steps {
		if err := c.CreateStepRun(ctx, sr); err != nil {
			t.Fatalf("create step run: %v", err)
		}
	}

	n, err := c.ReconcileOrphanStepRuns(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	// Only the running + pending steps under the interrupted instance.
	if n != 2 {
		t.Fatalf("expected 2 reconciled, got %d", n)
	}

	want := map[string]string{
		"sr_run":      StepStateInterrupted, // running under interrupted → interrupted
		"sr_pend":     StepStateInterrupted, // pending under interrupted → interrupted
		"sr_pass":     StepStatePassed,      // terminal, unchanged
		"sr_done_run": StepStateRunning,     // running but parent is done, untouched
	}
	got := map[string]StepRun{}
	for _, instID := range []string{"wf_interrupted", "wf_done"} {
		runs, err := c.ListStepRuns(ctx, instID)
		if err != nil {
			t.Fatalf("list step runs: %v", err)
		}
		for _, r := range runs {
			got[r.ID] = r
		}
	}
	for id, wantState := range want {
		if got[id].State != wantState {
			t.Errorf("%s: expected state %q, got %q", id, wantState, got[id].State)
		}
	}

	// A reconciled step should get a finished_at stamp so the dashboard can
	// render a duration instead of an open-ended in-progress step.
	if got["sr_run"].FinishedAt == nil {
		t.Error("sr_run: expected finished_at to be stamped on reconcile")
	}
}

func TestReconcileOrphanTaskCounters(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ts := c.InternalTasks()

	mkTask := func(id string, state model.TaskState, outstanding int) {
		t.Helper()
		if err := ts.CreateTask(ctx, &model.InternalTask{ID: id, Title: id}); err != nil {
			t.Fatalf("create task %s: %v", id, err)
		}
		if outstanding > 0 {
			if _, err := ts.IncrementOutstanding(ctx, id, outstanding); err != nil {
				t.Fatalf("increment %s: %v", id, err)
			}
		}
		if err := ts.UpdateTaskState(ctx, id, state); err != nil {
			t.Fatalf("set state %s: %v", id, err)
		}
	}
	taskState := func(id string) (string, int) {
		t.Helper()
		var st string
		var n int
		err := c.db.QueryRowContext(ctx,
			`SELECT state, COALESCE(outstanding_workflows,0) FROM internal_tasks WHERE id = ?`, id).Scan(&st, &n)
		if err != nil {
			t.Fatalf("read task %s: %v", id, err)
		}
		return st, n
	}

	// T1 — the issue #198 leak: an interrupted instance never decremented, a
	// later instance completed. Counter stuck at 1, task stuck 'running'.
	mkTask("T1", model.TaskStateRunning, 2)
	_ = c.CreateWorkflowInstance(ctx, &WorkflowInstance{ID: "t1-a", WorkflowID: "w", CellID: "1", TaskID: "T1", State: InstanceStateInterrupted})
	_ = c.CreateWorkflowInstance(ctx, &WorkflowInstance{ID: "t1-b", WorkflowID: "w", CellID: "1", TaskID: "T1", State: InstanceStateDone})
	if _, err := ts.DecrementOutstanding(ctx, "T1"); err != nil { // completeTask of t1-b
		t.Fatalf("decrement T1: %v", err)
	}

	// T2 — same leak but the completed instance of the current generation failed.
	mkTask("T2", model.TaskStateRunning, 2)
	_ = c.CreateWorkflowInstance(ctx, &WorkflowInstance{ID: "t2-a", WorkflowID: "w", CellID: "2", TaskID: "T2", State: InstanceStateInterrupted})
	_ = c.CreateWorkflowInstance(ctx, &WorkflowInstance{ID: "t2-b", WorkflowID: "w", CellID: "2", TaskID: "T2", State: InstanceStateFailed})
	if _, err := ts.DecrementOutstanding(ctx, "T2"); err != nil {
		t.Fatalf("decrement T2: %v", err)
	}

	// T3 — parked at approval: live instance, counter correct. Must be untouched.
	mkTask("T3", model.TaskStateRunning, 1)
	_ = c.CreateWorkflowInstance(ctx, &WorkflowInstance{ID: "t3-a", WorkflowID: "w", CellID: "3", TaskID: "T3", State: InstanceStateApprovalWaiting})

	// T4 — every current-generation instance interrupted: counter must drop to
	// zero but the task stays 'running' for the next poll to re-dispatch.
	mkTask("T4", model.TaskStateRunning, 1)
	_ = c.CreateWorkflowInstance(ctx, &WorkflowInstance{ID: "t4-a", WorkflowID: "w", CellID: "4", TaskID: "T4", State: InstanceStateInterrupted})

	// T5 — terminal task: out of scope, never touched even with a bogus counter.
	mkTask("T5", model.TaskStateDone, 3)

	recounted, settled, err := c.ReconcileOrphanTaskCounters(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if recounted != 3 { // T1, T2, T4 (T3 already correct, T5 terminal)
		t.Errorf("expected 3 recounted, got %d", recounted)
	}
	if settled != 2 { // T1 → done, T2 → failed
		t.Errorf("expected 2 settled, got %d", settled)
	}

	if st, n := taskState("T1"); st != string(model.TaskStateDone) || n != 0 {
		t.Errorf("T1: expected done/0, got %s/%d", st, n)
	}
	if st, n := taskState("T2"); st != string(model.TaskStateFailed) || n != 0 {
		t.Errorf("T2: expected failed/0, got %s/%d", st, n)
	}
	if st, n := taskState("T3"); st != string(model.TaskStateRunning) || n != 1 {
		t.Errorf("T3: expected running/1, got %s/%d", st, n)
	}
	if st, n := taskState("T4"); st != string(model.TaskStateRunning) || n != 0 {
		t.Errorf("T4: expected running/0, got %s/%d", st, n)
	}
	if st, n := taskState("T5"); st != string(model.TaskStateDone) || n != 3 {
		t.Errorf("T5: expected done/3 untouched, got %s/%d", st, n)
	}

	// Idempotent: a second pass finds nothing to repair.
	recounted, settled, err = c.ReconcileOrphanTaskCounters(ctx)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if recounted != 0 || settled != 0 {
		t.Errorf("expected idempotent no-op, got recounted=%d settled=%d", recounted, settled)
	}
}
