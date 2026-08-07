package daemon

import (
	"context"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/router"
)

// autoResumeKey keys the in-memory guard that stops a poll from dispatching a
// fresh instance of a workflow whose interrupted instance is being auto-resumed.
func autoResumeKey(taskID, workflowID string) string {
	return taskID + "\x00" + workflowID
}

// resumeAutoInterrupted replays instances left interrupted by a previous daemon
// process when their workflow opted into `resume: auto`.
//
// ReconcileOrphanWorkflowInstances flips every 'running' row to 'interrupted' at
// startup, which frees the task to be re-dispatched — but re-dispatch creates a
// brand-new instance starting at step 1, throwing away every step the killed run
// had already passed (issue #376: a completed implementation and a passed review
// were re-run after a restart, with duplicate-PR risk). `resume: auto` already
// declares the workflow safe to replay (config validation requires every step to
// be idempotent), so a restart should continue those runs rather than restart
// them: the resume descendant carries the passed steps over from cache and only
// re-runs the step that was in flight.
//
// Only `resume: auto` workflows are touched. Other policies keep today's
// behavior (re-dispatch from step 1, or a manual `apiary resume`); blocking them
// instead would strand every interrupted task until a human intervened.
//
// Called once at startup, after the reconcile passes (so orphans are already
// interrupted and task counters already recounted — StartResume increments the
// counter itself) and before the poll loops start.
func (d *Dispatcher) resumeAutoInterrupted(ctx context.Context) {
	if d.db == nil {
		return
	}
	instances, err := d.db.ListWorkflowInstancesByState(ctx, db.InstanceStateInterrupted)
	if err != nil {
		aplog.Warn("auto-resume interrupted instances: list: %v", err)
		return
	}

	// Oldest first, so keeping the last candidate per (task, workflow) leaves the
	// newest interrupted instance — the one carrying the most completed work.
	// Several interrupted rows can share a key when earlier restarts left orphans
	// that were never resumed.
	latest := map[string]db.WorkflowInstance{}
	var order []string
	for i := range instances {
		inst := instances[i]
		wf, ok := d.workflowByID(inst.WorkflowID)
		if !ok || wf.ResumePolicy() != config.ResumeAuto {
			continue
		}
		key := autoResumeKey(inst.TaskID, inst.WorkflowID)
		if _, seen := latest[key]; !seen {
			order = append(order, key)
		}
		latest[key] = inst
	}

	resumed := 0
	for _, key := range order {
		inst := latest[key]
		// A descendant already continued this instance (an earlier auto-resume, or
		// a manual `apiary resume`); replaying it again would duplicate the run.
		superseded, err := d.db.HasResumeDescendant(ctx, inst.ID)
		if err != nil {
			aplog.Warn("auto-resume %s: descendant check: %v", inst.ID, err)
			continue
		}
		if superseded {
			continue
		}
		if d.taskSettled(ctx, inst.TaskID) {
			continue
		}
		// Claim the (task, workflow) pair before launching, so a poll that fires
		// while the resume descendant is still being created does not dispatch a
		// fresh instance alongside it. Released when the resume goroutine returns;
		// by then the descendant row exists and dropActiveMatches takes over.
		if _, loaded := d.autoResuming.LoadOrStore(key, struct{}{}); loaded {
			continue
		}
		newID, err := d.startResume(ctx, inst.ID, ResumeOptions{}, func() { d.autoResuming.Delete(key) })
		if err != nil {
			d.autoResuming.Delete(key)
			aplog.Warn("auto-resume %s (workflow %s): %v — leaving interrupted", inst.ID, inst.WorkflowID, err)
			continue
		}
		aplog.Info("auto-resuming interrupted instance %s (workflow %s, resume: auto) as %s", inst.ID, inst.WorkflowID, newID)
		resumed++
	}
	if resumed > 0 {
		aplog.Info("auto-resumed %d interrupted workflow instance(s) from a previous run", resumed)
	}
}

// taskSettled reports whether an instance's task has already reached a terminal
// state, in which case there is nothing left to resume. An unknown/absent task
// (legacy instances predating task ids) is not settled.
func (d *Dispatcher) taskSettled(ctx context.Context, taskID string) bool {
	if taskID == "" || d.db == nil {
		return false
	}
	task, err := d.db.InternalTasks().GetTask(ctx, taskID)
	if err != nil || task == nil {
		return false
	}
	return task.State == model.TaskStateDone || task.State == model.TaskStateFailed
}

// dropAutoResumingMatches removes matches whose (task, workflow) is currently
// being auto-resumed at startup. It closes the window between claiming an
// interrupted instance and its resume descendant appearing in the DB: without
// it, the first poll after a restart could dispatch a fresh step-1 instance in
// parallel with the resume. Once the descendant row exists it is 'running' and
// dropActiveMatches keeps guarding it.
func (d *Dispatcher) dropAutoResumingMatches(taskID string, matches []router.Match) ([]router.Match, []droppedMatch) {
	out := make([]router.Match, 0, len(matches))
	var dropped []droppedMatch
	for _, m := range matches {
		if _, resuming := d.autoResuming.Load(autoResumeKey(taskID, m.Route.ID)); resuming {
			aplog.Debug("task %s: workflow %s is being auto-resumed, skipping re-dispatch", taskID, m.Route.ID)
			dropped = append(dropped, droppedMatch{WorkflowID: m.Route.ID, Reason: "auto-resuming"})
			continue
		}
		out = append(out, m)
	}
	return out, dropped
}
