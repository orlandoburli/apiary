package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/queue"
)

// redeliveredJob runs one poll, takes the job it enqueued for workflowID and
// returns it as a redelivery — the shape a job has after the daemon that leased
// it died and the next process reclaimed the expired lease.
func redeliveredJob(t *testing.T, d *Dispatcher, dbc *db.Client, workflowID string) (queue.Job, *model.InternalTask) {
	t.Helper()
	ctx := context.Background()
	d.poll(ctx, d.cfg.Sources[0], d.sources["src"], time.Time{})
	job := queueJobFor(t, dbc, workflowID)
	job.AttemptCount = 2
	return job, taskForCell(t, dbc, "src", "c1")
}

// TestExecuteQueuedJob_RedeliverySkippedWhileAutoResuming is the regression for
// issue #422: a daemon restart reclaims the lease of the job whose run it killed
// and re-delivers it, while startup is already continuing that run under
// `resume: auto`. Running the redelivery too puts a second agent on the same
// task, worktree and branch.
func TestExecuteQueuedJob_RedeliverySkippedWhileAutoResuming(t *testing.T) {
	ctx := context.Background()
	d, _, dbc := newQueueDispatcher(t, false)

	job, task := redeliveredJob(t, d, dbc, "triage")
	d.autoResuming.Store(autoResumeKey(task.ID, "triage"), struct{}{})

	res := d.ExecuteQueuedJob(ctx, job, "w1")
	if !res.Success {
		t.Fatalf("redelivery reported failure: %+v", res)
	}
	if got := len(instancesFor(t, dbc, task.ID, "triage")); got != 0 {
		t.Fatalf("redelivery started %d instance(s) alongside the auto-resume, want 0", got)
	}
}

// TestExecuteQueuedJob_RedeliverySkippedWithLiveInstance covers the same hole
// once the resume descendant (or a re-dispatch) is already recorded: the job's
// original run has been continued as a live instance, so re-running the payload
// would duplicate it.
func TestExecuteQueuedJob_RedeliverySkippedWithLiveInstance(t *testing.T) {
	ctx := context.Background()
	d, _, dbc := newQueueDispatcher(t, false)

	job, task := redeliveredJob(t, d, dbc, "triage")
	if err := dbc.CreateWorkflowInstance(ctx, &db.WorkflowInstance{
		ID: "i-live", WorkflowID: "triage", CellID: "c1", SourceID: "src",
		TaskID: task.ID, State: db.InstanceStateRunning,
	}); err != nil {
		t.Fatalf("create live instance: %v", err)
	}

	res := d.ExecuteQueuedJob(ctx, job, "w1")
	if !res.Success {
		t.Fatalf("redelivery reported failure: %+v", res)
	}
	if got := len(instancesFor(t, dbc, task.ID, "triage")); got != 1 {
		t.Fatalf("redelivery started a second instance alongside the live one (total %d, want 1)", got)
	}
}

// TestExecuteQueuedJob_RedeliveryRunsWhenNothingIsLive keeps the recovery path
// the guards must not break: a job whose run died with the daemon, with nothing
// continuing it, is re-delivered and runs.
func TestExecuteQueuedJob_RedeliveryRunsWhenNothingIsLive(t *testing.T) {
	ctx := context.Background()
	d, _, dbc := newQueueDispatcher(t, false)

	job, task := redeliveredJob(t, d, dbc, "triage")
	if err := dbc.CreateWorkflowInstance(ctx, &db.WorkflowInstance{
		ID: "i-orphan", WorkflowID: "triage", CellID: "c1", SourceID: "src",
		TaskID: task.ID, State: db.InstanceStateInterrupted,
	}); err != nil {
		t.Fatalf("create interrupted instance: %v", err)
	}

	res := d.ExecuteQueuedJob(ctx, job, "w1")
	if !res.Success {
		t.Fatalf("redelivery reported failure: %+v", res)
	}
	instances := instancesFor(t, dbc, task.ID, "triage")
	if len(instances) != 2 {
		t.Fatalf("redelivery produced %d instance(s), want 2 (the orphan plus the re-run)", len(instances))
	}
}
