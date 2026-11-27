package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// The gate's answers travel with the request and the chosen one is persisted
// with it, so an answer channel can render the choices and the audit trail
// records which one was picked rather than a binary restatement of it.
func TestApprovalOutcomesRoundTripAndResolve(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "outcomes.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	outcomes := []ApprovalOutcome{
		{Value: "approve", Label: "Ship it"},
		{Value: "rework", Goto: "implement"},
		{Value: "abort", Fail: true},
	}
	if err := c.CreateApprovalRequest(ctx, &ApprovalRequest{
		ID: "wf:gate", WorkflowInstanceID: "wf", StepID: "gate", Message: "ok",
		Outcomes: outcomes, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := c.GetApprovalRequest(ctx, "wf:gate")
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}
	if len(got.Outcomes) != 3 {
		t.Fatalf("outcomes = %+v, want 3", got.Outcomes)
	}
	if got.Outcomes[1].Goto != "implement" || !got.Outcomes[2].Fail {
		t.Errorf("outcomes lost their routing: %+v", got.Outcomes)
	}

	// Answering with a declared outcome records both the value and the binary
	// status the rest of the system already understands.
	req, resolved, err := c.ResolveApprovalRequest(ctx, "wf:gate", ApprovalResponse{
		Decision: "approve", Outcome: "rework", Actor: "alice", Channel: "cli",
		IdempotencyKey: "k1", Feedback: "needs another pass",
	})
	if err != nil || !resolved {
		t.Fatalf("resolve: resolved=%v err=%v", resolved, err)
	}
	if req.Outcome != "rework" {
		t.Errorf("request outcome = %q, want rework", req.Outcome)
	}
	if req.Feedback != "needs another pass" {
		t.Errorf("feedback = %q, want the reason", req.Feedback)
	}
	if req.Status != ApprovalApproved {
		t.Errorf("status = %q, want approved for a non-failing outcome", req.Status)
	}
}

// A gate that declares nothing keeps the classic pair and still records which
// half was chosen.
func TestApprovalWithoutDeclaredOutcomesRecordsTheDecision(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "classic.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	if err := c.CreateApprovalRequest(ctx, &ApprovalRequest{
		ID: "wf:gate", WorkflowInstanceID: "wf", StepID: "gate", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	req, resolved, err := c.ResolveApprovalRequest(ctx, "wf:gate", ApprovalResponse{
		Decision: "reject", Outcome: "reject", Actor: "alice", Channel: "cli", IdempotencyKey: "k1",
	})
	if err != nil || !resolved {
		t.Fatalf("resolve: resolved=%v err=%v", resolved, err)
	}
	if req.Status != ApprovalRejected || req.Outcome != "reject" {
		t.Errorf("status=%q outcome=%q, want rejected/reject", req.Status, req.Outcome)
	}
	if len(req.Outcomes) != 0 {
		t.Errorf("outcomes = %+v, want none declared", req.Outcomes)
	}
}
