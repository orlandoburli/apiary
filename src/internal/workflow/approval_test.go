package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

func TestEvaluateApproval_Conditions(t *testing.T) {
	step := config.StepConfig{
		Type:     config.StepTypeApproval,
		ResumeOn: &config.ApprovalTrigger{CommentContains: "approve", LabelAdded: "approved"},
		AbortOn:  &config.ApprovalTrigger{CommentContains: "reject", StateChanged: "cancelled"},
	}

	cases := []struct {
		name string
		cell model.SourceItem
		want ApprovalDecision
	}{
		{"no signal", model.SourceItem{}, ApprovalWait},
		{"resume by comment", model.SourceItem{Comments: []model.Comment{{Body: "looks good, APPROVE"}}}, ApprovalResume},
		{"resume by label", model.SourceItem{Labels: []string{"approved"}}, ApprovalResume},
		{"abort by comment", model.SourceItem{Comments: []model.Comment{{Body: "please reject this"}}}, ApprovalAbort},
		{"abort by state", model.SourceItem{State: "cancelled"}, ApprovalAbort},
		{"resume wins over abort", model.SourceItem{Comments: []model.Comment{{Body: "approve"}, {Body: "reject"}}}, ApprovalResume},
		{"unrelated comment waits", model.SourceItem{Comments: []model.Comment{{Body: "what about tests?"}}}, ApprovalWait},
	}
	for _, c := range cases {
		if got := EvaluateApproval(step, c.cell); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// approvalWorkflow: plan → approval → implement.
func approvalWorkflow() config.WorkflowConfig {
	return config.WorkflowConfig{ID: "feature", Steps: []config.StepConfig{
		{ID: "plan", Agent: "architect"},
		{ID: "gate", Type: config.StepTypeApproval, DependsOn: []string{"plan"},
			Message:  "Plan ready. Reply approve or reject.",
			ResumeOn: &config.ApprovalTrigger{CommentContains: "approve"},
			AbortOn:  &config.ApprovalTrigger{CommentContains: "reject"},
			Timeout:  "48h"},
		{ID: "implement", Agent: "backend-dev", DependsOn: []string{"gate"}},
	}}
}

func TestApproval_SuspendsAtApprovalStep(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{}
	side := &fakeSide{}
	eng := testEngine(cfg, store, exec, side)

	instID, success, _ := eng.RunInstance(context.Background(), approvalWorkflow(), model.InternalTask{ID: "c1"})
	if success {
		t.Fatal("a parked instance should not report success")
	}
	// Instance is parked in approval_waiting.
	if store.instances[instID].State != db.InstanceStateApprovalWaiting {
		t.Errorf("instance state = %q, want approval_waiting", store.instances[instID].State)
	}
	// The approval message was posted.
	if len(side.comments) != 1 || side.comments[0] != "Plan ready. Reply approve or reject." {
		t.Errorf("expected approval message posted, got %v", side.comments)
	}
	// "implement" must not have run yet; "plan" did.
	ids := executedIDs(exec.seen)
	if !contains(ids, "plan") || contains(ids, "implement") {
		t.Errorf("unexpected execution before approval: %v", ids)
	}
	// It shows up as a parked approval.
	parked := eng.ParkedApprovals()
	if len(parked) != 1 || parked[0].InstanceID != instID || parked[0].Step.ID != "gate" {
		t.Fatalf("expected one parked approval for gate, got %+v", parked)
	}
}

func TestApproval_ResumeContinuesWorkflow(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	instID, _, _ := eng.RunInstance(context.Background(), approvalWorkflow(), model.InternalTask{ID: "c1"})

	success, err := eng.ResolveApproval(context.Background(), instID, ApprovalResume)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !success {
		t.Fatal("expected success after resume")
	}
	if store.instances[instID].State != db.InstanceStateDone {
		t.Errorf("instance state = %q, want done", store.instances[instID].State)
	}
	if !contains(executedIDs(exec.seen), "implement") {
		t.Error("implement should run after approval resumed")
	}
	// No longer parked.
	if len(eng.ParkedApprovals()) != 0 {
		t.Error("instance should no longer be parked")
	}
}

func TestApproval_AbortFailsWorkflow(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	instID, _, _ := eng.RunInstance(context.Background(), approvalWorkflow(), model.InternalTask{ID: "c1"})

	success, _ := eng.ResolveApproval(context.Background(), instID, ApprovalAbort)
	if success {
		t.Fatal("expected failure after abort")
	}
	if store.instances[instID].State != db.InstanceStateFailed {
		t.Errorf("instance state = %q, want failed", store.instances[instID].State)
	}
	if contains(executedIDs(exec.seen), "implement") {
		t.Error("implement must not run after abort")
	}
}

func TestApproval_ResolveUnknownInstanceErrors(t *testing.T) {
	eng := testEngine(baseCfg(), newFakeStore(), &fakeExecutor{}, &fakeSide{})
	if _, err := eng.ResolveApproval(context.Background(), "ghost", ApprovalResume); err == nil {
		t.Error("expected error resolving an unparked instance")
	}
}

func TestApproval_CheckResumesOnComment(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	store.bindings["c1"] = []model.SourceBinding{{TaskID: "c1", SourceID: "s1", SourceItemID: "c1"}}
	instID, _, _ := eng.RunInstance(context.Background(), approvalWorkflow(), model.InternalTask{ID: "c1"})

	// Poll returns a cell with an approving comment.
	poll := func(sourceID, cellID string) (model.SourceItem, error) {
		return model.SourceItem{ID: cellID, SourceID: sourceID,
			Comments: []model.Comment{{Body: "approve please"}}}, nil
	}
	eng.CheckParkedApprovals(context.Background(), poll)

	if store.instances[instID].State != db.InstanceStateDone {
		t.Errorf("expected done after check resumed it, got %q", store.instances[instID].State)
	}
	if !contains(executedIDs(exec.seen), "implement") {
		t.Error("implement should have run after the check resumed the approval")
	}
}

func TestApproval_CheckTimesOut(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{}

	// Controllable clock: park at t0, then advance well past the 48h timeout.
	clock := time.Unix(0, 0)
	eng := NewEngine(cfg, store, exec,
		WithSideEffects(&fakeSide{}),
		WithClock(func() time.Time { return clock }),
		WithIDGen(func(p string) string { return p + "-1" }),
	)

	instID, _, _ := eng.RunInstance(context.Background(), approvalWorkflow(), model.InternalTask{ID: "c1"})

	// Advance clock past 48h; poll returns nothing actionable.
	clock = time.Unix(0, 0).Add(49 * time.Hour)
	noSignal := func(sourceID, cellID string) (model.SourceItem, error) {
		return model.SourceItem{ID: cellID}, nil
	}
	eng.CheckParkedApprovals(context.Background(), noSignal)

	if store.instances[instID].State != db.InstanceStateFailed {
		t.Errorf("expected failed after timeout, got %q", store.instances[instID].State)
	}
}

func TestApproval_CheckWaitsWhenNoSignal(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	eng := testEngine(cfg, store, &fakeExecutor{}, &fakeSide{})

	store.bindings["c1"] = []model.SourceBinding{{TaskID: "c1", SourceID: "s1", SourceItemID: "c1"}}
	instID, _, _ := eng.RunInstance(context.Background(), approvalWorkflow(), model.InternalTask{ID: "c1"})

	poll := func(sourceID, cellID string) (model.SourceItem, error) {
		return model.SourceItem{ID: cellID, Comments: []model.Comment{{Body: "still thinking"}}}, nil
	}
	eng.CheckParkedApprovals(context.Background(), poll)

	// Still parked, still approval_waiting.
	if store.instances[instID].State != db.InstanceStateApprovalWaiting {
		t.Errorf("expected still approval_waiting, got %q", store.instances[instID].State)
	}
	if len(eng.ParkedApprovals()) != 1 {
		t.Error("instance should remain parked")
	}
}
