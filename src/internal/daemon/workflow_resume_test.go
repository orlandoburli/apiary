package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/state"
)

func TestResumableState(t *testing.T) {
	// The canonical vocabulary files interruption, approval waits and CI waits
	// all under "blocked", so the reason is what separates a resumable orphan
	// from a live park (#465).
	cases := []struct {
		st     string
		reason string
		want   bool
	}{
		{db.InstanceStateFailed, "", true},
		{db.InstanceStateBlocked, string(state.ReasonInterrupted), true},
		{db.InstanceStateBlocked, string(state.ReasonApproval), false},
		{db.InstanceStateBlocked, string(state.ReasonCI), false},
		{db.InstanceStateBlocked, "", false},
		{db.InstanceStateDone, "", false},
		{db.InstanceStateRunning, "", false},
		{db.InstanceStateQueued, "", false},
	}
	for _, c := range cases {
		if got := resumableState(c.st, c.reason); got != c.want {
			t.Errorf("resumableState(%q, %q) = %v, want %v", c.st, c.reason, got, c.want)
		}
	}
}

func TestResumeHTTPStatus(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{ErrInstanceNotFound, http.StatusNotFound},
		{ErrInstanceNotResumable, http.StatusConflict},
		{ErrResumeForbidden, http.StatusConflict},
		{ErrWorkflowChanged, http.StatusUnprocessableEntity},
		{errOther{}, http.StatusInternalServerError},
	}
	for _, c := range cases {
		if got := resumeHTTPStatus(c.err); got != c.want {
			t.Errorf("resumeHTTPStatus(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

type errOther struct{}

func (errOther) Error() string { return "other" }

func TestWorkflowByID(t *testing.T) {
	d := &Dispatcher{cfg: &config.Config{
		Workflows: []config.WorkflowConfig{
			{ID: "feature-development"},
			{ID: "backend-bugs", Steps: []config.StepConfig{{ID: "run", Agent: "backend-dev"}}},
		},
	}}

	if wf, ok := d.workflowByID("feature-development"); !ok || wf.ID != "feature-development" {
		t.Errorf("declared workflow not resolved: %+v ok=%v", wf, ok)
	}
	if wf, ok := d.workflowByID("backend-bugs"); !ok || len(wf.Steps) != 1 {
		t.Errorf("workflow not resolved: %+v ok=%v", wf, ok)
	}
	if _, ok := d.workflowByID("ghost"); ok {
		t.Error("unknown id should not resolve")
	}
}

func TestResumePreviewUsesOriginalSnapshotAndFromStep(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "resume.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbc.Close() })
	original := config.WorkflowConfig{ID: "feature", Steps: []config.StepConfig{{ID: "plan", Agent: "a"}, {ID: "build", Agent: "b"}}}
	b, _ := json.Marshal(original)
	inst := &db.WorkflowInstance{ID: "source", WorkflowID: original.ID, CellID: "1", State: db.InstanceStateFailed}
	if err := dbc.CreateWorkflowInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if err := dbc.PutWorkflowSnapshot(ctx, inst.ID, string(b)); err != nil {
		t.Fatal(err)
	}
	if err := dbc.CreateStepRun(ctx, &db.StepRun{ID: "plan", WorkflowInstanceID: inst.ID, StepID: "plan", State: db.StepStatePassed}); err != nil {
		t.Fatal(err)
	}
	d := &Dispatcher{db: dbc, cfg: &config.Config{Workflows: []config.WorkflowConfig{{ID: "feature", Steps: []config.StepConfig{{ID: "replacement"}}}}}}
	preview, err := d.ResumePreview(ctx, inst.ID, ResumeOptions{FromStep: "build", ConfigMode: ResumeConfigOriginal})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Skip) != 1 || preview.Skip[0].StepID != "plan" {
		t.Errorf("skip = %+v", preview.Skip)
	}
	if len(preview.Run) != 1 || preview.Run[0].StepID != "build" {
		t.Errorf("run = %+v", preview.Run)
	}
}
