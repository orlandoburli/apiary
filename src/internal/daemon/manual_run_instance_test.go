package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/queue"
)

// instanceRow fetches an instance that must exist.
func instanceRow(t *testing.T, dbc *db.Client, id string) *db.WorkflowInstance {
	t.Helper()
	inst, err := dbc.GetWorkflowInstance(context.Background(), id)
	if err != nil {
		t.Fatalf("get instance %s: %v", id, err)
	}
	if inst == nil {
		t.Fatalf("instance %q does not exist", id)
	}
	return inst
}

// taskInstanceIDs lists every instance recorded for the task.
func taskInstanceIDs(t *testing.T, dbc *db.Client, taskID string) []string {
	t.Helper()
	insts, err := dbc.ListWorkflowInstancesByTask(context.Background(), taskID)
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	ids := make([]string, 0, len(insts))
	for _, inst := range insts {
		ids = append(ids, inst.ID)
	}
	return ids
}

func outstanding(t *testing.T, dbc *db.Client, taskID string) int {
	t.Helper()
	task, err := dbc.InternalTasks().GetTask(context.Background(), taskID)
	if err != nil || task == nil {
		t.Fatalf("get task %s: %v", taskID, err)
	}
	return task.OutstandingWorkflows
}

// startManual runs `impl` on the harness item, which waits in the queue.
func startManual(t *testing.T, d *Dispatcher) ManualRunResult {
	t.Helper()
	res, err := d.RunWorkflowManual(context.Background(), ManualRunRequest{WorkflowID: "impl", ItemRef: "c1"})
	if err != nil {
		t.Fatalf("manual run: %v", err)
	}
	if res.InstanceID == "" {
		t.Fatal("result carries no instance id")
	}
	return res
}

// The whole of issue #504: the id comes back while the run is still waiting in the
// queue, and it already resolves — in the list and by id.
func TestRunWorkflowManual_InstanceExistsWhenItReturns(t *testing.T) {
	ctx := context.Background()
	d, adapter, dbc := manualDispatcher(t)
	d.poll(ctx, d.cfg.Sources[0], adapter, time.Time{})

	res := startManual(t, d)

	if got := instanceRow(t, dbc, res.InstanceID).State; got != db.InstanceStateQueued {
		t.Errorf("state = %q, want queued: nothing has leased the job yet", got)
	}
	list, err := d.Instances(ctx, "", "impl", 10, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Instances) != 1 || list.Instances[0].ID != res.InstanceID {
		t.Errorf("listed %+v, want only %s", list.Instances, res.InstanceID)
	}
	detail, err := d.InstanceDetail(ctx, res.InstanceID)
	if err != nil || detail == nil {
		t.Fatalf("detail = %+v, %v", detail, err)
	}
	if detail.CellID != "c1" {
		t.Errorf("cell = %q, want c1, the same view the engine would record", detail.CellID)
	}
}

func TestManualRunHandler_ReturnsInstanceID(t *testing.T) {
	ctx := context.Background()
	d, _, dbc := manualDispatcher(t)

	w := httptest.NewRecorder()
	d.manualRunHandler(ctx)(w, httptest.NewRequest(http.MethodPost, "/workflows/triage/run", nil))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	var res ManualRunResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(res.InstanceID, "wf_") {
		t.Fatalf("instance_id = %q, want a wf_ id; body=%s", res.InstanceID, w.Body.String())
	}
	instanceRow(t, dbc, res.InstanceID)
}

// The id handed out must be the run's, not a neighbour: delivering the job adopts
// the reserved row instead of creating a second one.
func TestQueuedManualRun_AdoptsReservedInstance(t *testing.T) {
	ctx := context.Background()
	d, adapter, dbc := manualDispatcher(t)
	d.poll(ctx, d.cfg.Sources[0], adapter, time.Time{})
	res := startManual(t, d)

	job := queueJobFor(t, dbc, "impl")
	job.AttemptCount = 1
	d.ExecuteQueuedJob(ctx, job, "w1")

	if got := instanceRow(t, dbc, res.InstanceID).State; got == db.InstanceStateQueued {
		t.Error("the run did not take over its reserved row")
	}
	steps, err := dbc.ListStepRuns(ctx, res.InstanceID)
	if err != nil || len(steps) == 0 {
		t.Errorf("reserved instance has %d step run(s), err %v — the work ran elsewhere", len(steps), err)
	}
	insts, err := dbc.ListWorkflowInstancesByTask(ctx, res.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	impl := 0
	for _, inst := range insts {
		if inst.WorkflowID == "impl" {
			impl++
		}
	}
	if impl != 1 {
		t.Errorf("%d impl instance(s), want exactly the reserved one", impl)
	}
}

// Without a queue the run goes through fan-out's goroutine: same id, same row.
func TestInlineManualRun_AdoptsReservedInstance(t *testing.T) {
	ctx := context.Background()
	d, _, dbc := manualDispatcher(t)
	d.dispatchQueue = nil

	res, err := d.RunWorkflowManual(ctx, ManualRunRequest{WorkflowID: "triage"})
	if err != nil {
		t.Fatalf("manual run: %v", err)
	}
	d.bg.Wait()

	if got := instanceRow(t, dbc, res.InstanceID).State; got == db.InstanceStateQueued {
		t.Error("the run did not take over its reserved row")
	}
	if ids := taskInstanceIDs(t, dbc, res.TaskID); len(ids) != 1 || ids[0] != res.InstanceID {
		t.Errorf("instances = %v, want only %s", ids, res.InstanceID)
	}
}

// Cancelling a run that has not started must stop it starting, release the task,
// and leave nothing for the job's settlement to count a second time.
func TestStopInstance_CancelsQueuedReservation(t *testing.T) {
	ctx := context.Background()
	d, adapter, dbc := manualDispatcher(t)
	d.poll(ctx, d.cfg.Sources[0], adapter, time.Time{})
	res := startManual(t, d)
	before := outstanding(t, dbc, res.TaskID)

	job := queueJobFor(t, dbc, "impl") // as leased before the cancel landed
	if err := d.StopInstance(ctx, res.InstanceID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if got := instanceRow(t, dbc, res.InstanceID).State; got != db.InstanceStateCanceled {
		t.Fatalf("state = %q, want canceled", got)
	}
	if got := outstanding(t, dbc, res.TaskID); got != before-1 {
		t.Errorf("outstanding = %d, want %d", got, before-1)
	}

	job.AttemptCount = 1
	if finish := d.ExecuteQueuedJob(ctx, job, "w1"); !finish.Success {
		t.Errorf("finish = %+v: a cancelled run is not a failed dispatch", finish)
	}
	if steps, _ := dbc.ListStepRuns(ctx, res.InstanceID); len(steps) != 0 {
		t.Errorf("a cancelled reservation ran %d step(s)", len(steps))
	}

	job.State = queue.JobCanceled
	if err := d.settleRemoteQueueJob(ctx, job); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if got := outstanding(t, dbc, res.TaskID); got != before-1 {
		t.Errorf("outstanding after settle = %d, want %d — counted twice", got, before-1)
	}
}

// A remote worker keeps its own database, so the control plane's row is still
// queued when the job finishes. It takes the job's outcome, once, under the id the
// caller was given — not a synthetic queue-<jobid> beside it.
func TestSettleQueueJob_ResolvesReservation(t *testing.T) {
	for _, tc := range []struct {
		job  queue.JobState
		want string
	}{
		{queue.JobSucceeded, db.InstanceStateDone},
		{queue.JobFailed, db.InstanceStateFailed},
		{queue.JobCanceled, db.InstanceStateCanceled},
	} {
		t.Run(string(tc.job), func(t *testing.T) {
			ctx := context.Background()
			d, adapter, dbc := manualDispatcher(t)
			d.poll(ctx, d.cfg.Sources[0], adapter, time.Time{})
			res := startManual(t, d)
			before := outstanding(t, dbc, res.TaskID)

			job := queueJobFor(t, dbc, "impl")
			job.State = tc.job
			for i := 0; i < 2; i++ { // settling is re-run at every startup
				if err := d.settleRemoteQueueJob(ctx, job); err != nil {
					t.Fatalf("settle: %v", err)
				}
			}
			if got := instanceRow(t, dbc, res.InstanceID).State; got != tc.want {
				t.Errorf("state = %q, want %q", got, tc.want)
			}
			if got := outstanding(t, dbc, res.TaskID); got != before-1 {
				t.Errorf("outstanding = %d, want %d", got, before-1)
			}
			if inst, _ := dbc.GetWorkflowInstance(ctx, "queue-"+job.ID); inst != nil {
				t.Error("wrote a phantom queue-<jobid> instance beside the reservation")
			}
		})
	}
}

// The reserved row predates its job, so the created_at test that recognises a
// locally-owned run cannot see it. An adopted row must still stop settlement.
func TestSettleQueueJob_LeavesAdoptedInstanceAlone(t *testing.T) {
	ctx := context.Background()
	d, adapter, dbc := manualDispatcher(t)
	d.poll(ctx, d.cfg.Sources[0], adapter, time.Time{})
	res := startManual(t, d)
	before := outstanding(t, dbc, res.TaskID)

	if ok, err := dbc.LeaveQueuedState(ctx, res.InstanceID, db.InstanceStateRunning, ""); !ok || err != nil {
		t.Fatalf("claim: %v, %v", ok, err)
	}
	job := queueJobFor(t, dbc, "impl")
	job.State = queue.JobFailed
	if err := d.settleRemoteQueueJob(ctx, job); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if got := instanceRow(t, dbc, res.InstanceID).State; got != db.InstanceStateRunning {
		t.Errorf("state = %q, want running: the engine owns this row", got)
	}
	if got := outstanding(t, dbc, res.TaskID); got != before {
		t.Errorf("outstanding = %d, want %d", got, before)
	}
	if inst, _ := dbc.GetWorkflowInstance(ctx, "queue-"+job.ID); inst != nil {
		t.Error("wrote a phantom queue-<jobid> instance")
	}
}

// A redelivery after the first attempt died finds the row already used. It runs as
// a new instance; refusing would silently drop the retry.
func TestQueuedManualRun_RedeliveryRunsAsNewInstance(t *testing.T) {
	ctx := context.Background()
	d, adapter, dbc := manualDispatcher(t)
	d.poll(ctx, d.cfg.Sources[0], adapter, time.Time{})
	res := startManual(t, d)

	if ok, err := dbc.LeaveQueuedState(ctx, res.InstanceID, db.InstanceStateBlocked, "interrupted"); !ok || err != nil {
		t.Fatalf("interrupt: %v, %v", ok, err)
	}
	before := len(taskInstanceIDs(t, dbc, res.TaskID))
	job := queueJobFor(t, dbc, "impl")
	job.AttemptCount = 2
	d.ExecuteQueuedJob(ctx, job, "w1")

	if got := len(taskInstanceIDs(t, dbc, res.TaskID)); got != before+1 {
		t.Errorf("%d instance(s), want %d: the retry must run", got, before+1)
	}
}

// failingQueue refuses every enqueue.
type failingQueue struct{ queue.Store }

func (failingQueue) Enqueue(context.Context, *queue.Job) (bool, error) {
	return false, errors.New("disk full")
}

// A run that never reached the queue must fail the command rather than hand back
// an id for something that cannot start.
func TestRunWorkflowManual_EnqueueFailureIsAnError(t *testing.T) {
	ctx := context.Background()
	d, adapter, dbc := manualDispatcher(t)
	d.poll(ctx, d.cfg.Sources[0], adapter, time.Time{})
	d.dispatchQueue = failingQueue{d.dispatchQueue}

	res, err := d.RunWorkflowManual(ctx, ManualRunRequest{WorkflowID: "impl", ItemRef: "c1"})
	if !errors.Is(err, ErrNotDispatched) {
		t.Fatalf("err = %v, want ErrNotDispatched", err)
	}
	if got := instanceRow(t, dbc, res.InstanceID).State; got != db.InstanceStateCanceled {
		t.Errorf("state = %q, want canceled", got)
	}
}

// Automatic dispatch reserves nothing: its instances still appear only when the
// run starts, and its payload is unchanged.
func TestPollDispatch_ReservesNothing(t *testing.T) {
	ctx := context.Background()
	d, adapter, dbc := manualDispatcher(t)
	d.poll(ctx, d.cfg.Sources[0], adapter, time.Time{})

	job := queueJobFor(t, dbc, "triage")
	if strings.Contains(string(job.Payload), "instance_id") {
		t.Errorf("automatic payload carries an instance id: %s", job.Payload)
	}
	if ids := taskInstanceIDs(t, dbc, job.TaskID); len(ids) != 0 {
		t.Errorf("poll created instances %v before any run started", ids)
	}
}
