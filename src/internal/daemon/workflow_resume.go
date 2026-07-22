package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/source"
)

// Resume error sentinels. They map to CLI exit codes / HTTP statuses:
// not-found → 2/404, not-resumable → 3/409, workflow-changed → 4/422.
var (
	ErrInstanceNotFound     = errors.New("instance not found")
	ErrInstanceNotResumable = errors.New("instance is not resumable")
	ErrResumeForbidden      = errors.New("workflow resume policy forbids replay")
	ErrWorkflowChanged      = errors.New("workflow definition changed or removed")
	ErrSnapshotNotFound     = errors.New("original workflow snapshot not found")
)

const (
	ResumeConfigCurrent  = "current"
	ResumeConfigOriginal = "original"
)

type ResumeOptions struct {
	FromStep   string
	ConfigMode string
}

// ResumeStep is one entry in a resume preview's skip/run list.
type ResumeStep struct {
	StepID string `json:"step_id"`
	Agent  string `json:"agent"`
	Note   string `json:"note,omitempty"`
}

// ResumePreview describes what a resume would skip (already passed) and run.
type ResumePreview struct {
	InstanceID string       `json:"instance_id"`
	Workflow   string       `json:"workflow"`
	CellID     string       `json:"cell_id"`
	Title      string       `json:"title"`
	State      string       `json:"state"`
	FromStep   string       `json:"from_step,omitempty"`
	ConfigMode string       `json:"config_mode"`
	Skip       []ResumeStep `json:"skip"`
	Run        []ResumeStep `json:"run"`
}

// resumableState reports whether an instance in this state can be resumed.
// Only failed and interrupted instances qualify; done/running/approval_waiting/
// pending are rejected (approval_waiting resumes via the polling loop, not here).
func resumableState(state string) bool {
	return state == db.InstanceStateFailed || state == db.InstanceStateInterrupted
}

// workflowByID resolves a workflow definition by id, covering both declared
func (d *Dispatcher) workflowByID(id string) (config.WorkflowConfig, bool) {
	for i := range d.cfg.Workflows {
		if d.cfg.Workflows[i].ID == id {
			return d.cfg.Workflows[i], true
		}
	}
	return config.WorkflowConfig{}, false
}

// ResumePreview validates resumability and computes the skip/run breakdown for
// the confirmation prompt. It returns a typed error for each rejection reason.
func (d *Dispatcher) ResumePreview(ctx context.Context, id string, opts ResumeOptions) (*ResumePreview, error) {
	if d.db == nil {
		return nil, ErrInstanceNotFound
	}
	inst, err := d.db.GetWorkflowInstance(ctx, id)
	if err != nil {
		return nil, err
	}
	if inst == nil {
		return nil, ErrInstanceNotFound
	}
	if !resumableState(inst.State) {
		return nil, ErrInstanceNotResumable
	}
	wf, err := d.resumeWorkflow(ctx, inst, opts.ConfigMode)
	if err != nil {
		return nil, err
	}
	if wf.ResumePolicy() == config.ResumeForbidden {
		return nil, ErrResumeForbidden
	}

	steps, err := d.db.ListStepRuns(ctx, id)
	if err != nil {
		return nil, err
	}
	passed := map[string]bool{}
	for _, s := range steps {
		if s.State == db.StepStatePassed {
			passed[s.StepID] = true
		}
	}

	title, _ := d.db.GetTaskTitle(ctx, inst.CellID)
	preview := &ResumePreview{
		InstanceID: inst.ID,
		Workflow:   inst.WorkflowID,
		CellID:     inst.CellID,
		Title:      title,
		State:      inst.State,
		FromStep:   opts.FromStep,
		ConfigMode: normalizeResumeConfigMode(opts.ConfigMode),
	}
	fromIndex := len(wf.Steps)
	if opts.FromStep != "" {
		fromIndex = -1
		for i, st := range wf.Steps {
			if st.ID == opts.FromStep {
				fromIndex = i
				break
			}
		}
		if fromIndex < 0 {
			return nil, fmt.Errorf("%w: resume step %q not found", ErrWorkflowChanged, opts.FromStep)
		}
	}
	for i, st := range wf.Steps {
		// Splits are re-evaluated on resume; list them under "run" since they
		// re-execute (cheaply, with no side effects).
		if i < fromIndex && passed[st.ID] && st.StepType() != config.StepTypeSplit {
			preview.Skip = append(preview.Skip, ResumeStep{StepID: st.ID, Agent: st.Agent, Note: "already passed"})
		} else {
			preview.Run = append(preview.Run, ResumeStep{StepID: st.ID, Agent: st.Agent})
		}
	}
	return preview, nil
}

// StartResume validates the instance and launches the resume on the supplied
// (daemon-lifetime) context. It returns promptly; the engine replays cached
// steps and continues the run in the background. ctx must outlive the request.
func (d *Dispatcher) StartResume(ctx context.Context, id string, opts ResumeOptions) (string, error) {
	if d.db == nil {
		return "", ErrInstanceNotFound
	}
	inst, err := d.db.GetWorkflowInstance(ctx, id)
	if err != nil {
		return "", err
	}
	if inst == nil {
		return "", ErrInstanceNotFound
	}
	if !resumableState(inst.State) {
		return "", ErrInstanceNotResumable
	}
	wf, err := d.resumeWorkflow(ctx, inst, opts.ConfigMode)
	if err != nil {
		return "", err
	}
	if wf.ResumePolicy() == config.ResumeForbidden {
		return "", ErrResumeForbidden
	}
	if opts.FromStep != "" {
		found := false
		for _, step := range wf.Steps {
			if step.ID == opts.FromStep {
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("%w: resume step %q not found", ErrWorkflowChanged, opts.FromStep)
		}
	}
	steps, err := d.db.ListStepRuns(ctx, id)
	if err != nil {
		return "", err
	}
	task := d.taskForInstance(ctx, inst)
	if task.ID != "" {
		if _, err := d.db.InternalTasks().IncrementOutstanding(ctx, task.ID, 1); err != nil {
			return "", err
		}
	}
	newID := d.workflowEngine().NewInstanceID()

	go func() {
		_, success, rerr := d.workflowEngine().ResumeInstance(ctx, inst, wf, task, steps, opts.FromStep, newID)
		if rerr != nil {
			if task.ID != "" {
				_, _ = d.db.InternalTasks().DecrementOutstanding(ctx, task.ID)
			}
			aplog.Error("resume %s: %v", id, rerr)
			return
		}
		aplog.Info("resume %s: descendant %s finished (success=%v; may be awaiting approval)", id, newID, success)
	}()
	return newID, nil
}

func normalizeResumeConfigMode(mode string) string {
	if mode == ResumeConfigOriginal {
		return mode
	}
	return ResumeConfigCurrent
}

func (d *Dispatcher) resumeWorkflow(ctx context.Context, inst *db.WorkflowInstance, mode string) (config.WorkflowConfig, error) {
	if normalizeResumeConfigMode(mode) == ResumeConfigCurrent {
		wf, ok := d.workflowByID(inst.WorkflowID)
		if !ok {
			return config.WorkflowConfig{}, ErrWorkflowChanged
		}
		return wf, nil
	}
	snapshot, err := d.db.GetWorkflowSnapshot(ctx, inst.ID)
	if err != nil {
		return config.WorkflowConfig{}, err
	}
	if snapshot == "" {
		return config.WorkflowConfig{}, ErrSnapshotNotFound
	}
	var wf config.WorkflowConfig
	if err := json.Unmarshal([]byte(snapshot), &wf); err != nil {
		return config.WorkflowConfig{}, ErrWorkflowChanged
	}
	if current, ok := d.workflowByID(inst.WorkflowID); ok {
		wf.Env = current.Env
		stepEnv := make(map[string]map[string]string, len(current.Steps))
		for _, step := range current.Steps {
			stepEnv[step.ID] = step.Env
		}
		for i := range wf.Steps {
			wf.Steps[i].Env = stepEnv[wf.Steps[i].ID]
		}
	}
	return wf, nil
}

// ResolveResumeTarget finds the most recent resumable instance of a workflow,
// for the `apiary resume --workflow <id>` form.
func (d *Dispatcher) ResolveResumeTarget(ctx context.Context, workflowID string) (string, error) {
	if d.db == nil {
		return "", ErrInstanceNotFound
	}
	inst, err := d.db.LatestResumableInstance(ctx, workflowID)
	if err != nil {
		return "", err
	}
	if inst == nil {
		return "", ErrInstanceNotFound
	}
	return inst.ID, nil
}

// resumeHTTPStatus maps a resume error to its IPC HTTP status.
func resumeHTTPStatus(err error) int {
	switch {
	case errors.Is(err, ErrInstanceNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrInstanceNotResumable):
		return http.StatusConflict
	case errors.Is(err, ErrResumeForbidden):
		return http.StatusConflict
	case errors.Is(err, ErrWorkflowChanged):
		return http.StatusUnprocessableEntity
	case errors.Is(err, ErrSnapshotNotFound):
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}

// taskForInstance resolves the InternalTask a resume runs against. When the
// instance carries a task id (the post-Phase-5 path) the persisted task is
// loaded so the engine can resolve its source bindings for side effects. A
// legacy instance with no task id falls back to a transient task synthesized
// from the live/stored cell (no bindings → side effects are skipped on resume).
func (d *Dispatcher) taskForInstance(ctx context.Context, inst *db.WorkflowInstance) model.InternalTask {
	if inst.TaskID != "" {
		if t, err := d.db.InternalTasks().GetTask(ctx, inst.TaskID); err == nil && t != nil {
			return *t
		}
	}
	return transientTask(d.cellForInstance(ctx, inst))
}

// cellForInstance rebuilds the cell to resume against: a fresh poll when the
// source supports it, otherwise a minimal cell from the stored id/title.
func (d *Dispatcher) cellForInstance(ctx context.Context, inst *db.WorkflowInstance) model.SourceItem {
	if adapter, ok := d.sources[inst.SourceID]; ok {
		if poller, ok := adapter.(source.TaskPoller); ok {
			if c, err := poller.PollTask(ctx, inst.CellID); err == nil {
				return c
			}
		}
	}
	title, _ := d.db.GetTaskTitle(ctx, inst.CellID)
	return model.SourceItem{ID: inst.CellID, SourceID: inst.SourceID, Title: title}
}
