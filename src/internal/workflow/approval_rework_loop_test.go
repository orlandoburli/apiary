package workflow

import (
	"context"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

// An approval step sitting inside a rework loop is re-entered on every lap of the
// same workflow instance. Each re-entry must mint a fresh, answerable request:
// under the old (instance, step) uniqueness the engine resurfaced lap 1's already
// resolved row, so the human's lap-2 answer bounced off with "already approved"
// and the instance wedged for good (#462).
func TestApprovalInsideReworkLoopIsAnswerableOnEveryLap(t *testing.T) {
	ctx := context.Background()
	client := realDB(t)

	exec := newSeqExecutor()
	exec.scripts["verify"] = []StepResult{
		{Success: false, Output: "needs rework"}, // lap 1 → loop back past the gate
		{Success: true, Output: "LGTM"},          // lap 2 → done
	}
	eng := realEngine(t, client, exec)

	wf := config.WorkflowConfig{
		ID: "rework-gate",
		Steps: []config.StepConfig{
			{ID: "implement", Agent: "agent-a"},
			// Explicit approvers keep the gate waiting for a persisted response
			// rather than a source comment, which is the path the CLI and the
			// dashboard both take.
			{ID: "pre-review", Type: config.StepTypeApproval, DependsOn: []string{"implement"},
				Message: "Ready for review?", Approvers: []string{"alice"}, RequiredApprovals: 1},
			{ID: "verify", Agent: "agent-b", DependsOn: []string{"pre-review"},
				OnFail: &config.StepOutcome{Goto: "implement", MaxRetries: 2}},
		},
	}

	instID, success, err := eng.RunInstance(ctx, wf, model.InternalTask{ID: "T-462"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if success {
		t.Fatal("instance parked at the gate should not report success")
	}

	answer := func(lap int, key string) *db.ApprovalRequest {
		t.Helper()
		req, err := client.GetApprovalByInstance(ctx, instID)
		if err != nil || req == nil {
			t.Fatalf("lap %d: request=%+v err=%v", lap, req, err)
		}
		if req.Status != db.ApprovalPending {
			t.Fatalf("lap %d: request %s is %q, want pending — the human cannot answer it", lap, req.ID, req.Status)
		}
		response := db.ApprovalResponse{Decision: "approve", Actor: "alice", Channel: "cli", IdempotencyKey: key}
		if _, won, err := client.ResolveApprovalRequest(ctx, req.ID, response); err != nil || !won {
			t.Fatalf("lap %d: resolving %s: won=%v err=%v", lap, req.ID, won, err)
		}
		if _, err := eng.ResolveApprovalResponse(ctx, instID, response); err != nil {
			t.Fatalf("lap %d: advance: %v", lap, err)
		}
		return req
	}

	lap1 := answer(1, "cli-lap-1")
	if lap1.Attempt != 1 || lap1.ID != instID+":pre-review" {
		t.Fatalf("lap 1 request=%s attempt=%d", lap1.ID, lap1.Attempt)
	}

	// verify failed and looped back past the gate: the instance is parked again,
	// this time on a request of its own.
	if parked := eng.ParkedApprovals(); len(parked) != 1 || parked[0].Step.ID != "pre-review" {
		t.Fatalf("after lap 1: parked=%+v, want one park at pre-review", parked)
	}
	lap2 := answer(2, "cli-lap-2")
	if lap2.Attempt != 2 || lap2.ID == lap1.ID {
		t.Fatalf("lap 2 request=%s attempt=%d, want a fresh request distinct from %s", lap2.ID, lap2.Attempt, lap1.ID)
	}

	inst, err := client.GetWorkflowInstance(ctx, instID)
	if err != nil {
		t.Fatal(err)
	}
	if inst.State != db.InstanceStateDone {
		t.Fatalf("instance state=%q, want done — the second lap should have run to completion", inst.State)
	}

	// Both decisions survive as separate audit rows.
	for _, want := range []string{lap1.ID, lap2.ID} {
		stored, err := client.GetApprovalRequest(ctx, want)
		if err != nil || stored == nil || stored.Status != db.ApprovalApproved {
			t.Fatalf("request %s=%+v err=%v, want an approved row per lap", want, stored, err)
		}
	}
}
