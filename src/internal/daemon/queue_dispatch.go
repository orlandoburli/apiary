package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/queue"
	"github.com/orlandoburli/apiary/internal/router"
	"github.com/orlandoburli/apiary/internal/worker"
)

const dispatchPayloadVersion = 1

type dispatchJobPayload struct {
	Version int                `json:"version"`
	Cell    model.SourceItem   `json:"cell"`
	Task    model.InternalTask `json:"task"`
	Match   router.Match       `json:"match"`
}

func (d *Dispatcher) configureDispatchQueue() error {
	if d.db == nil || !d.cfg.Settings.Queue.IsEnabled() {
		return nil
	}
	d.dispatchQueue = d.db.Queue()
	d.queueProjectID = strings.TrimSpace(d.cfg.Settings.Queue.ProjectID)
	if d.queueProjectID == "" {
		absolute, _ := filepath.Abs(d.configFile)
		d.queueProjectID = filepath.Dir(absolute)
	}
	if !d.cfg.Settings.Queue.UsesEmbeddedWorker() {
		return nil
	}
	workerID := strings.TrimSpace(d.cfg.Settings.Queue.WorkerID)
	if workerID == "" {
		workerID = defaultQueueWorkerID(d.queueProjectID)
	}
	d.queueWorkerID = workerID
	labels := append([]string{"local", "trusted", runtime.GOOS}, d.cfg.Settings.Queue.WorkerLabels...)
	capabilities := append([]string{"apiary.workflow"}, d.cfg.Settings.Queue.WorkerCapabilities...)
	for _, adapter := range d.agentRunner {
		capabilities = append(capabilities, "runner:"+adapter)
	}
	d.queueWorker = queue.Worker{ID: workerID, ProtocolVersion: queue.WorkerProtocolVersion, Pool: defaultString(d.cfg.Settings.Queue.WorkerPool, "default"), Labels: uniqueStrings(labels), Capabilities: uniqueStrings(capabilities), Capacity: d.cfg.Settings.Queue.WorkerCapacityValue(), Ready: true}
	runtimeWorker, err := worker.New(d.dispatchQueue, worker.ExecutorFunc(d.executeQueuedJob), worker.Config{
		Worker:        d.queueWorker,
		LeaseDuration: d.cfg.Settings.Queue.LeaseDurationValue(), HeartbeatInterval: d.cfg.Settings.Queue.HeartbeatIntervalValue(), WorkerHeartbeat: d.cfg.Settings.Queue.HeartbeatIntervalValue(), WorkerTimeout: d.cfg.Settings.Queue.WorkerTimeoutValue(), PollInterval: d.cfg.Settings.Queue.PollIntervalValue(), Policy: d.queuePolicy(),
		OnError: func(err error) { aplog.Error("embedded worker: %v", err) },
	})
	if err != nil {
		return fmt.Errorf("configure embedded worker: %w", err)
	}
	d.localWorker = runtimeWorker
	return nil
}

func (d *Dispatcher) queuePolicy() queue.ConcurrencyPolicy {
	configured := d.cfg.Settings.Queue.Concurrency
	policy := queue.ConcurrencyPolicy{DefaultProject: configured.DefaultProject, Projects: configured.Projects, DefaultSource: configured.DefaultSource, Sources: configured.Sources, DefaultAgent: configured.DefaultAgent, Agents: copyLimits(configured.Agents), DefaultRunner: configured.DefaultRunner, Runners: configured.Runners, DefaultPool: configured.DefaultPool, Pools: configured.Pools}
	if policy.DefaultProject == 0 {
		policy.DefaultProject = d.cfg.Settings.Concurrency
	}
	if policy.Agents == nil {
		policy.Agents = map[string]int{}
	}
	for _, agent := range d.cfg.Agents {
		if _, explicit := policy.Agents[agent.ID]; !explicit && agent.MaxWorkers > 0 {
			policy.Agents[agent.ID] = agent.MaxWorkers
		}
	}
	return policy
}

func (d *Dispatcher) enqueueFanOut(ctx context.Context, cell model.SourceItem, task model.InternalTask, matches []router.Match) {
	if _, err := d.db.InternalTasks().IncrementOutstanding(ctx, task.ID, len(matches)); err != nil {
		aplog.Error("task %s: increment outstanding by %d before enqueue: %v", task.ID, len(matches), err)
		return
	}
	generation, err := d.db.InternalTasks().Generation(ctx, task.ID)
	if err != nil {
		aplog.Error("task %s: read dispatch generation: %v", task.ID, err)
	}
	for _, match := range matches {
		payload, err := json.Marshal(dispatchJobPayload{Version: dispatchPayloadVersion, Cell: cell, Task: task, Match: match})
		if err != nil {
			d.rollbackQueuedOutstanding(ctx, task.ID, fmt.Errorf("marshal dispatch payload: %w", err))
			continue
		}
		pool, labels, capabilities, affinity := d.requirementsForMatch(task.ID, match)
		job := &queue.Job{IdempotencyKey: fmt.Sprintf("%s:%d:%s", task.ID, generation, match.Route.ID), ProjectID: d.queueProjectID, SourceID: cell.SourceID, TaskID: task.ID, WorkflowID: match.Route.ID, AgentID: match.Route.Agent, RunnerID: d.agentRunner[match.Route.Agent], Pool: pool, RequiredLabels: labels, RequiredCapabilities: capabilities, AffinityKey: affinity, PayloadVersion: dispatchPayloadVersion, Payload: payload, Priority: -match.Route.Priority, MaxAttempts: d.cfg.Settings.MaxAttempts}
		created, err := d.dispatchQueue.Enqueue(ctx, job)
		if err != nil {
			d.rollbackQueuedOutstanding(ctx, task.ID, fmt.Errorf("enqueue workflow %s: %w", match.Route.ID, err))
			continue
		}
		if !created {
			d.rollbackQueuedOutstanding(ctx, task.ID, nil)
			continue
		}
		d.recordExecutionEvent(ctx, db.ExecutionEvent{Type: "queue.enqueued", TaskID: task.ID, WorkflowID: match.Route.ID, Metadata: map[string]any{"job_id": job.ID, "generation": generation, "pool": pool, "required_labels": labels, "required_capabilities": capabilities}})
	}
}

func (d *Dispatcher) rollbackQueuedOutstanding(ctx context.Context, taskID string, err error) {
	_, _ = d.db.InternalTasks().DecrementOutstanding(ctx, taskID)
	if err != nil {
		aplog.Error("task %s: %v", taskID, err)
	}
}

func (d *Dispatcher) executeQueuedJob(ctx context.Context, job queue.Job) queue.FinishResult {
	return d.ExecuteQueuedJob(ctx, job, d.queueWorkerID)
}

// ExecuteQueuedJob executes a protocol job using this dispatcher's configured
// runners. Remote workers use this entry point without polling project sources.
func (d *Dispatcher) ExecuteQueuedJob(ctx context.Context, job queue.Job, workerID string) queue.FinishResult {
	var payload dispatchJobPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return queue.FinishResult{Error: fmt.Sprintf("decode dispatch payload: %v", err)}
	}
	if payload.Version != dispatchPayloadVersion {
		return queue.FinishResult{Error: fmt.Sprintf("unsupported dispatch payload version %d", payload.Version)}
	}
	if d.db != nil && payload.Task.ID != "" {
		completed, err := d.db.HasCompletedInstanceForRoute(ctx, payload.Task.ID, payload.Match.Route.ID)
		if err != nil {
			return queue.FinishResult{Error: fmt.Sprintf("check completed workflow: %v", err), Retry: true}
		}
		if completed {
			return queue.FinishResult{Success: true}
		}
	}
	d.active.Add(1)
	defer d.active.Add(-1)
	d.activeRuns.Store(job.ID, model.ActiveRun{ID: job.ID, Cell: payload.Cell, WorkerID: workerID, Model: payload.Match.Worker.Model, Status: model.RunStatusRunning, StartedAt: time.Now()})
	defer d.activeRuns.Delete(job.ID)
	result := d.dispatch(ctx, payload.Cell, nil, payload.Task, payload.Match)
	if ctx.Err() != nil {
		return queue.FinishResult{Error: ctx.Err().Error()}
	}
	if !result.Success {
		return queue.FinishResult{Error: "workflow dispatch failed"}
	}
	return queue.FinishResult{Success: true}
}

// settleRemoteQueueJob mirrors the terminal workflow state into the canonical
// control-plane database. The remote worker keeps detailed step state locally;
// this summary makes task accounting and re-dispatch guards durable centrally.
func (d *Dispatcher) settleRemoteQueueJob(ctx context.Context, job queue.Job) error {
	if d.db == nil || job.TaskID == "" {
		return nil
	}
	instanceID := "queue-" + job.ID
	if existing, err := d.db.GetWorkflowInstance(ctx, instanceID); err != nil {
		return err
	} else if existing != nil {
		return nil
	}
	// An embedded worker persists its real instance and settles the task itself.
	instances, err := d.db.ListWorkflowInstancesByTask(ctx, job.TaskID)
	if err != nil {
		return err
	}
	for _, instance := range instances {
		if instance.WorkflowID == job.WorkflowID && !instance.CreatedAt.Before(job.CreatedAt) && (instance.State == db.InstanceStateDone || instance.State == db.InstanceStateFailed || instance.State == db.InstanceStateInterrupted) {
			return nil
		}
	}
	state := db.InstanceStateFailed
	if job.State == queue.JobSucceeded {
		state = db.InstanceStateDone
	}
	if job.State == queue.JobCanceled {
		state = db.InstanceStateInterrupted
	}
	if err := d.db.CreateWorkflowInstance(ctx, &db.WorkflowInstance{ID: instanceID, WorkflowID: job.WorkflowID, TaskID: job.TaskID, SourceID: job.SourceID, State: state}); err != nil {
		return err
	}
	remaining, err := d.db.InternalTasks().DecrementOutstanding(ctx, job.TaskID)
	if err != nil || remaining > 0 {
		return err
	}
	if job.State == queue.JobCanceled {
		return nil
	}
	failed := job.State == queue.JobFailed
	if !failed {
		failed, err = d.db.HasFailedInstance(ctx, job.TaskID)
		if err != nil {
			return err
		}
	}
	taskState := model.TaskStateDone
	if failed {
		taskState = model.TaskStateFailed
	}
	return d.db.InternalTasks().UpdateTaskState(ctx, job.TaskID, taskState)
}

// warnUnsatisfiableQueueJobs logs a warning for queued jobs whose pool, labels,
// or required capabilities no registered worker (including the embedded one)
// can satisfy. Without it this failure mode is completely silent: jobs sit
// queued forever while `apiary status` shows a ready worker with free capacity.
func (d *Dispatcher) warnUnsatisfiableQueueJobs(ctx context.Context) {
	if d.dispatchQueue == nil {
		return
	}
	jobs, err := d.dispatchQueue.ListJobs(ctx, queue.JobQueued, 1000)
	if err != nil {
		aplog.Error("list queued jobs for capability check: %v", err)
		return
	}
	if len(jobs) == 0 {
		return
	}
	workers, err := d.dispatchQueue.ListWorkers(ctx)
	if err != nil {
		aplog.Error("list workers for capability check: %v", err)
		return
	}
	// The embedded worker registers asynchronously in Start; include its spec
	// so its own jobs are never reported as unsatisfiable during startup.
	if d.localWorker != nil {
		workers = append(workers, d.queueWorker)
	}
	unsatisfiable := 0
	for _, job := range jobs {
		if !jobSatisfiableByAnyWorker(job, workers) {
			unsatisfiable++
			aplog.Warn("queued job %s (task %s, workflow %s) is not satisfiable by any registered worker: pool=%q required_labels=%v required_capabilities=%v", job.ID, job.TaskID, job.WorkflowID, job.Pool, job.RequiredLabels, job.RequiredCapabilities)
		}
	}
	if unsatisfiable > 0 {
		aplog.Warn("%d queued job(s) cannot be leased by any registered worker — they will stay queued until a worker advertising the required pool/labels/capabilities registers", unsatisfiable)
	}
}

func jobSatisfiableByAnyWorker(job queue.Job, workers []queue.Worker) bool {
	for _, w := range workers {
		if job.Pool != "" && job.Pool != w.Pool {
			continue
		}
		if hasAllStrings(w.Labels, job.RequiredLabels) && hasAllStrings(w.Capabilities, job.RequiredCapabilities) {
			return true
		}
	}
	return false
}

func hasAllStrings(have, need []string) bool {
	set := make(map[string]bool, len(have))
	for _, value := range have {
		set[value] = true
	}
	for _, value := range need {
		if !set[value] {
			return false
		}
	}
	return true
}

func (d *Dispatcher) reconcileTerminalQueueJobs(ctx context.Context) {
	if d.dispatchQueue == nil {
		return
	}
	for _, state := range []queue.JobState{queue.JobSucceeded, queue.JobFailed, queue.JobCanceled} {
		jobs, err := d.dispatchQueue.ListJobs(ctx, state, 1000)
		if err != nil {
			aplog.Error("reconcile %s queue jobs: %v", state, err)
			continue
		}
		for _, job := range jobs {
			if err := d.settleRemoteQueueJob(ctx, job); err != nil {
				aplog.Error("settle queue job %s: %v", job.ID, err)
			}
		}
	}
}

func (d *Dispatcher) requirementsForMatch(taskID string, match router.Match) (string, []string, []string, string) {
	agents := []string{match.Route.Agent}
	for _, workflow := range d.cfg.Workflows {
		if workflow.ID == match.Route.ID {
			agents = workflowAgentIDs(workflow.Steps)
			break
		}
	}
	pool, affinity := "default", ""
	var labels, capabilities []string
	for _, id := range uniqueStrings(agents) {
		for _, agent := range d.cfg.Agents {
			if agent.ID != id {
				continue
			}
			if agent.WorkerPool != "" {
				pool = agent.WorkerPool
			}
			labels = append(labels, agent.RequiresLabels...)
			capabilities = append(capabilities, agent.RequiresCapabilities...)
			if adapter := d.agentRunner[id]; adapter != "" {
				capabilities = append(capabilities, "runner:"+adapter)
			}
			if agent.WorkspaceAffinity {
				affinity = "task:" + taskID
			}
		}
	}
	capabilities = append(capabilities, "apiary.workflow")
	return pool, uniqueStrings(labels), uniqueStrings(capabilities), affinity
}

func workflowAgentIDs(steps []config.StepConfig) []string {
	var result []string
	for _, step := range steps {
		if step.Agent != "" {
			result = append(result, step.Agent)
		}
		if step.Step != nil {
			result = append(result, workflowAgentIDs([]config.StepConfig{*step.Step})...)
		}
		result = append(result, workflowAgentIDs(step.ParallelSteps)...)
	}
	return result
}

func defaultQueueWorkerID(project string) string {
	host, _ := os.Hostname()
	if host == "" {
		host = "localhost"
	}
	digest := sha256.Sum256([]byte(project))
	return host + "-" + hex.EncodeToString(digest[:4])
}

func uniqueStrings(values []string) []string {
	set := map[string]bool{}
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = true
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
func copyLimits(values map[string]int) map[string]int {
	if values == nil {
		return nil
	}
	result := make(map[string]int, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
