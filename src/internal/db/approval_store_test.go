package db

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestApprovalRequestAtomicIdempotentResolution(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	req := &ApprovalRequest{ID: "inst:gate", WorkflowInstanceID: "inst", TaskID: "task", WorkflowID: "deploy", StepID: "gate", Approvers: []string{"alice"}, Fields: []map[string]any{{"name": "ticket", "required": true}}}
	if err := c.CreateApprovalRequest(ctx, req); err != nil {
		t.Fatal(err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, won, err := c.ResolveApprovalRequest(ctx, req.ID, ApprovalResponse{Decision: "approve", Actor: "alice", Channel: "webhook", IdempotencyKey: fmt.Sprintf("key-%d", i), Values: map[string]any{"ticket": "OPS-7"}})
			if err != nil {
				t.Errorf("resolve: %v", err)
			}
			if won {
				wins.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("wins=%d, want 1", wins.Load())
	}
	stored, err := c.GetApprovalRequest(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != ApprovalApproved || stored.RespondedBy != "alice" {
		t.Fatalf("stored=%+v", stored)
	}
}

func TestApprovalRequestDuplicateIdempotencyKeyDoesNotAdvanceAnotherRequest(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	for _, id := range []string{"one", "two"} {
		if err := c.CreateApprovalRequest(ctx, &ApprovalRequest{ID: id, WorkflowInstanceID: id, StepID: "gate"}); err != nil {
			t.Fatal(err)
		}
	}
	response := ApprovalResponse{Decision: "approve", Actor: "alice", Channel: "webhook", IdempotencyKey: "delivery-1"}
	if _, won, err := c.ResolveApprovalRequest(ctx, "one", response); err != nil || !won {
		t.Fatalf("first: won=%v err=%v", won, err)
	}
	_, won, err := c.ResolveApprovalRequest(ctx, "two", response)
	if err != nil || won {
		t.Fatalf("duplicate: won=%v err=%v", won, err)
	}
	two, _ := c.GetApprovalRequest(ctx, "two")
	if two.Status != ApprovalPending {
		t.Fatalf("second status=%s", two.Status)
	}
}

func TestApprovalRequestRequiresConfiguredQuorum(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	req := &ApprovalRequest{ID: "quorum", WorkflowInstanceID: "quorum-inst", StepID: "gate", Approvers: []string{"alice", "bob"}, RequiredApprovals: 2}
	if err := c.CreateApprovalRequest(ctx, req); err != nil {
		t.Fatal(err)
	}
	stored, resolved, err := c.ResolveApprovalRequest(ctx, req.ID, ApprovalResponse{Decision: "approve", Actor: "alice", Channel: "webhook", IdempotencyKey: "a"})
	if err != nil || resolved || stored.Status != ApprovalPending {
		t.Fatalf("first response resolved=%v status=%s err=%v", resolved, stored.Status, err)
	}
	stored, resolved, err = c.ResolveApprovalRequest(ctx, req.ID, ApprovalResponse{Decision: "approve", Actor: "bob", Channel: "dashboard", IdempotencyKey: "b"})
	if err != nil || !resolved || stored.Status != ApprovalApproved {
		t.Fatalf("quorum resolved=%v status=%s err=%v", resolved, stored.Status, err)
	}
}

func TestApprovalReminderAndEscalationAreIdempotent(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	req := &ApprovalRequest{ID: "timers", WorkflowInstanceID: "timers", StepID: "gate"}
	if err := c.CreateApprovalRequest(ctx, req); err != nil {
		t.Fatal(err)
	}
	if won, err := c.MarkApprovalReminded(ctx, req.ID); err != nil || !won {
		t.Fatalf("reminder won=%v err=%v", won, err)
	}
	if won, _ := c.MarkApprovalReminded(ctx, req.ID); won {
		t.Fatal("duplicate reminder won")
	}
	if won, err := c.EscalateApproval(ctx, req.ID); err != nil || !won {
		t.Fatalf("escalation won=%v err=%v", won, err)
	}
	stored, _ := c.GetApprovalRequest(ctx, req.ID)
	if stored.Status != ApprovalEscalated || stored.RemindedAt == nil || stored.EscalatedAt == nil {
		t.Fatalf("stored=%+v", stored)
	}
}
