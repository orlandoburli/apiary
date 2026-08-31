package db

import (
	"context"
	"testing"
)

// A rework loop re-enters the same approval step on the same workflow instance.
// Each visit must mint its own answerable request instead of resurfacing the
// previous lap's resolved one, which used to leave the human's decision bouncing
// off with "already approved" forever (#462).
func TestCreateApprovalRequestMintsFreshAttemptPerLap(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	lap1 := &ApprovalRequest{ID: "inst:pre-review", WorkflowInstanceID: "inst", StepID: "pre-review", Message: "review?"}
	if err := c.CreateApprovalRequest(ctx, lap1); err != nil {
		t.Fatal(err)
	}
	if lap1.ID != "inst:pre-review" || lap1.Attempt != 1 {
		t.Fatalf("lap 1: id=%q attempt=%d, want inst:pre-review/1", lap1.ID, lap1.Attempt)
	}

	// Still open: re-creating hands back the same gate, never a second one.
	again := &ApprovalRequest{ID: "inst:pre-review", WorkflowInstanceID: "inst", StepID: "pre-review", Message: "review?"}
	if err := c.CreateApprovalRequest(ctx, again); err != nil {
		t.Fatal(err)
	}
	if again.ID != lap1.ID || again.Attempt != 1 {
		t.Fatalf("re-entry while open: id=%q attempt=%d, want the open request back", again.ID, again.Attempt)
	}

	if _, won, err := c.ResolveApprovalRequest(ctx, lap1.ID, ApprovalResponse{Decision: "approve", Actor: "alice", Channel: "cli", IdempotencyKey: "k1", Values: map[string]any{"decision": "rework"}}); err != nil || !won {
		t.Fatalf("resolve lap 1: won=%v err=%v", won, err)
	}

	lap2 := &ApprovalRequest{ID: "inst:pre-review", WorkflowInstanceID: "inst", StepID: "pre-review", Message: "review?"}
	if err := c.CreateApprovalRequest(ctx, lap2); err != nil {
		t.Fatal(err)
	}
	if lap2.Attempt != 2 || lap2.ID != "inst:pre-review@2" {
		t.Fatalf("lap 2: id=%q attempt=%d, want inst:pre-review@2/2", lap2.ID, lap2.Attempt)
	}
	if lap2.Status != ApprovalPending {
		t.Fatalf("lap 2 status=%q, want pending", lap2.Status)
	}

	// The instance is parked on the newest lap, so that is what the pollers see.
	current, err := c.GetApprovalByInstance(ctx, "inst")
	if err != nil {
		t.Fatal(err)
	}
	if current == nil || current.ID != lap2.ID {
		t.Fatalf("GetApprovalByInstance=%+v, want %s", current, lap2.ID)
	}

	// And it is answerable: lap 1's decision does not block it.
	if _, won, err := c.ResolveApprovalRequest(ctx, lap2.ID, ApprovalResponse{Decision: "reject", Actor: "alice", Channel: "cli", IdempotencyKey: "k2"}); err != nil || !won {
		t.Fatalf("resolve lap 2: won=%v err=%v", won, err)
	}
	if stored, _ := c.GetApprovalRequest(ctx, lap1.ID); stored.Status != ApprovalApproved {
		t.Fatalf("lap 1 status=%q, want the earlier decision preserved", stored.Status)
	}
}

// A database created before the attempt column carries the narrow
// UNIQUE(workflow_instance_id, step_id) constraint, which no ALTER can widen.
// InitSchema must rebuild the table, keeping the existing rows and the responses
// that reference them by id.
func TestInitSchemaWidensLegacyApprovalUniqueness(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	// Recreate the pre-#462 shape underneath the migrated one.
	legacy := []string{
		`DROP TABLE approval_requests`,
		`CREATE TABLE approval_requests (
		  id TEXT PRIMARY KEY,
		  workflow_instance_id TEXT NOT NULL,
		  task_id TEXT, workflow_id TEXT, step_id TEXT NOT NULL, message TEXT,
		  approvers TEXT NOT NULL DEFAULT '[]', delegates TEXT NOT NULL DEFAULT '{}', required_approvals INTEGER NOT NULL DEFAULT 1, fields TEXT NOT NULL DEFAULT '[]',
		  status TEXT NOT NULL DEFAULT 'pending', response_values TEXT NOT NULL DEFAULT '{}',
		  feedback TEXT, responded_by TEXT, response_channel TEXT,
		  idempotency_key TEXT UNIQUE, created_at DATETIME NOT NULL, expires_at DATETIME,
		  reminded_at DATETIME, escalated_at DATETIME, responded_at DATETIME,
		  UNIQUE(workflow_instance_id, step_id)
		)`,
		`INSERT INTO approval_requests (id, workflow_instance_id, step_id, status, created_at)
		 VALUES ('inst:gate', 'inst', 'gate', 'approved', '2026-08-01 10:00:00')`,
	}
	for _, stmt := range legacy {
		if _, err := c.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if legacyKey, err := hasLegacyApprovalUnique(ctx, c.db); err != nil || !legacyKey {
		t.Fatalf("setup: legacy=%v err=%v", legacyKey, err)
	}

	if err := InitSchema(ctx, c.db); err != nil {
		t.Fatal(err)
	}
	if legacyKey, err := hasLegacyApprovalUnique(ctx, c.db); err != nil || legacyKey {
		t.Fatalf("after migrate: legacy=%v err=%v", legacyKey, err)
	}
	stored, err := c.GetApprovalRequest(ctx, "inst:gate")
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.Attempt != 1 || stored.Status != ApprovalApproved {
		t.Fatalf("carried row=%+v, want the approved lap 1 with attempt 1", stored)
	}

	// The whole point: a second lap now fits.
	lap2 := &ApprovalRequest{ID: "inst:gate", WorkflowInstanceID: "inst", StepID: "gate"}
	if err := c.CreateApprovalRequest(ctx, lap2); err != nil {
		t.Fatal(err)
	}
	if lap2.ID != "inst:gate@2" {
		t.Fatalf("lap 2 id=%q, want inst:gate@2", lap2.ID)
	}
}
