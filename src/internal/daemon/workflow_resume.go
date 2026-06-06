package daemon

import (
	"context"
	"errors"
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
	ErrWorkflowChanged      = errors.New("workflow definition changed or removed")
)

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
func (d *Dispatcher) ResumePreview(ctx context.Context, id string) (*ResumePreview, error) {
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
	wf, ok := d.workflowByID(inst.WorkflowID)
	if !ok {
		return nil, ErrWorkflowChanged
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
	}
	for _, st := range wf.Steps {
		// Splits are re-evaluated on resume; list them under "run" since they
		// re-execute (cheaply, with no side effects).
		if passed[st.ID] && st.StepType() != config.StepTypeSplit {
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
func (d *Dispatcher) StartResume(ctx context.Context, id string) error {
	if d.db == nil {
		return ErrInstanceNotFound
	}
	inst, err := d.db.GetWorkflowInstance(ctx, id)
	if err != nil {
		return err
	}
	if inst == nil {
		return ErrInstanceNotFound
	}
	if !resumableState(inst.State) {
		return ErrInstanceNotResumable
	}
	wf, ok := d.workflowByID(inst.WorkflowID)
	if !ok {
		return ErrWorkflowChanged
	}
	steps, err := d.db.ListStepRuns(ctx, id)
	if err != nil {
		return err
	}
	cell := d.cellForInstance(ctx, inst)

	go func() {
		success, rerr := d.workflowEngine().ResumeInstance(ctx, id, wf, cell, steps)
		if rerr != nil {
			aplog.Error("resume %s: %v", id, rerr)
			return
		}
		aplog.Info("resume %s: finished (success=%v; may be awaiting approval)", id, success)
	}()
	return nil
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
	case errors.Is(err, ErrWorkflowChanged):
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
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
