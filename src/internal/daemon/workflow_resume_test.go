package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
)

func TestResumableState(t *testing.T) {
	cases := map[string]bool{
		db.InstanceStateFailed:          true,
		db.InstanceStateInterrupted:     true,
		db.InstanceStateDone:            false,
		db.InstanceStateRunning:         false,
		db.InstanceStateApprovalWaiting: false,
		db.InstanceStatePending:         false,
	}
	for state, want := range cases {
		if got := resumableState(state); got != want {
			t.Errorf("resumableState(%q) = %v, want %v", state, got, want)
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
