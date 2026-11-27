package daemon

import (
	"testing"

	"github.com/orlandoburli/apiary/internal/db"
)

// When a gate declares outcomes, the outcome is the answer and the binary
// decision is derived from it: a `fail` outcome must route down the rejection
// path the engine and the store already understand.
func TestValidateApprovalResponseDerivesDecisionFromOutcome(t *testing.T) {
	request := &db.ApprovalRequest{Outcomes: []db.ApprovalOutcome{
		{Value: "approve", Label: "Ship it"},
		{Value: "rework", Goto: "implement"},
		{Value: "abort", Fail: true},
	}}

	cases := []struct {
		outcome  string
		decision string
	}{
		{"approve", "approve"},
		{"rework", "approve"},
		{"abort", "reject"},
	}
	for _, tc := range cases {
		resp := &db.ApprovalResponse{Outcome: tc.outcome, Actor: "alice"}
		if err := validateApprovalResponse(request, resp); err != nil {
			t.Fatalf("outcome %q: %v", tc.outcome, err)
		}
		if resp.Decision != tc.decision {
			t.Errorf("outcome %q derived decision %q, want %q", tc.outcome, resp.Decision, tc.decision)
		}
	}
}

// An answer that is not one of the declared outcomes is refused: the workflow
// defines the answers, so an invented one has no meaning downstream.
func TestValidateApprovalResponseRejectsAnUndeclaredOutcome(t *testing.T) {
	request := &db.ApprovalRequest{Outcomes: []db.ApprovalOutcome{{Value: "approve"}, {Value: "reject", Fail: true}}}
	resp := &db.ApprovalResponse{Outcome: "maybe", Actor: "alice"}
	if err := validateApprovalResponse(request, resp); err == nil {
		t.Fatal("an undeclared outcome was accepted")
	}
}

// A gate without declared outcomes keeps the classic validation.
func TestValidateApprovalResponseKeepsTheClassicPair(t *testing.T) {
	request := &db.ApprovalRequest{}
	if err := validateApprovalResponse(request, &db.ApprovalResponse{Decision: "approve", Actor: "alice"}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if err := validateApprovalResponse(request, &db.ApprovalResponse{Decision: "maybe", Actor: "alice"}); err == nil {
		t.Fatal("an unknown decision was accepted on a gate with no outcomes")
	}
}
