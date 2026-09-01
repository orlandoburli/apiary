package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/state"
)

// TestInstanceDetail_IncludesCIPolls verifies the wait_for CI poll history is
// carried into the instance detail view (the payload for `apiary instances <id>`),
// oldest-first.
func TestInstanceDetail_IncludesCIPolls(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "detail.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	mustCreateInstance(t, dbc, &db.WorkflowInstance{
		ID: "wi_ci", WorkflowID: "implementation", CellID: "42", State: db.InstanceStateBlocked, BlockedReason: string(state.ReasonCI),
	})
	if err := dbc.CreateStepRun(ctx, &db.StepRun{
		ID: "wi_ci-implement", WorkflowInstanceID: "wi_ci", StepID: "implement", AgentID: "engineer", State: db.StepStatePassed,
	}); err != nil {
		t.Fatalf("create step: %v", err)
	}
	for _, st := range []string{"pending", "pending", "passed"} {
		if err := dbc.RecordCIPollCheck(ctx, &db.CIPollCheck{
			WorkflowInstanceID: "wi_ci", StepID: "check-ci", Status: st, PRURL: "https://x/pr/9",
		}); err != nil {
			t.Fatalf("record poll: %v", err)
		}
	}

	detail, err := (&Dispatcher{db: dbc}).InstanceDetail(ctx, "wi_ci")
	if err != nil {
		t.Fatalf("InstanceDetail: %v", err)
	}
	if detail == nil {
		t.Fatal("expected detail, got nil")
	}
	if len(detail.CIPolls) != 3 {
		t.Fatalf("CIPolls = %d, want 3", len(detail.CIPolls))
	}
	if detail.CIPolls[0].Status != "pending" || detail.CIPolls[2].Status != "passed" {
		t.Errorf("poll ordering wrong: %q … %q", detail.CIPolls[0].Status, detail.CIPolls[2].Status)
	}
	if detail.CIPolls[2].StepID != "check-ci" || detail.CIPolls[2].PRURL != "https://x/pr/9" {
		t.Errorf("poll fields not carried: %+v", detail.CIPolls[2])
	}
}
