package daemon

import (
	"net/http"
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
		Workflows: []config.WorkflowConfig{{ID: "feature-development"}},
		Routes:    []config.RouteConfig{{ID: "backend-bugs", Agent: "backend-dev"}},
	}}

	if wf, ok := d.workflowByID("feature-development"); !ok || wf.ID != "feature-development" {
		t.Errorf("declared workflow not resolved: %+v ok=%v", wf, ok)
	}
	// A plain route resolves to a synthesized single-step workflow.
	if wf, ok := d.workflowByID("backend-bugs"); !ok || len(wf.Steps) != 1 {
		t.Errorf("route not synthesized into a workflow: %+v ok=%v", wf, ok)
	}
	if _, ok := d.workflowByID("ghost"); ok {
		t.Error("unknown id should not resolve")
	}
}
