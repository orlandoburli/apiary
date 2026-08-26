package dashboard

import (
	"context"
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/db"
)

func newFormApp() *App {
	return &App{model: &Model{width: 100, height: 40}}
}

func fieldRequest() *db.ApprovalRequest {
	return &db.ApprovalRequest{
		ID:      "wf-1:gate",
		Message: "Release 2.4 is staged. How should it go out?",
		Fields: []map[string]any{
			{"name": "strategy", "label": "Rollout strategy", "type": "choice",
				"options": []any{"canary", "blue_green", "full"}, "required": true},
			{"name": "change_ticket", "type": "string", "required": true},
			{"name": "urgent", "type": "boolean"},
		},
	}
}

// y answers a field-less gate in one key, but a gate that declares fields opens
// the form — the values are part of the answer and cannot be guessed.
func TestAnswerApprovalRouting(t *testing.T) {
	a := newFormApp()
	if _, cmd := a.answerApproval(&db.ApprovalRequest{ID: "wf:gate"}, "y"); cmd == nil {
		t.Fatal("y on a field-less gate should post a response")
	}
	if a.model.approvalActive {
		t.Fatal("a field-less gate should not open the form")
	}

	a = newFormApp()
	if _, cmd := a.answerApproval(fieldRequest(), "y"); cmd != nil {
		t.Fatal("y on a gate with fields should open the form, not post")
	}
	if !a.model.approvalActive {
		t.Fatal("expected the form to open")
	}

	// n rejects outright even with fields: a refusal never fills in a form.
	a = newFormApp()
	if _, cmd := a.answerApproval(fieldRequest(), "n"); cmd == nil {
		t.Fatal("n should post a rejection directly")
	}
	if a.model.approvalActive {
		t.Fatal("rejection should not open the form")
	}
}

// Every field type must round-trip from keystrokes to a well-typed payload.
func TestApprovalFormCollectsTypedValues(t *testing.T) {
	a := newFormApp()
	a.openApprovalForm(fieldRequest())
	fields := approvalFields(a.model.approvalReq)

	// A choice defaults to its first option and cycles with the arrow keys.
	if got := a.model.approvalVals["strategy"]; got != "canary" {
		t.Fatalf("choice should seed to the first option, got %v", got)
	}
	a.editApprovalField(fields[0], "right")
	if got := a.model.approvalVals["strategy"]; got != "blue_green" {
		t.Fatalf("right should advance the choice, got %v", got)
	}
	a.editApprovalField(fields[0], "3")
	if got := a.model.approvalVals["strategy"]; got != "full" {
		t.Fatalf("a digit should select that option, got %v", got)
	}

	// A text field takes printable runes and honours backspace.
	for _, key := range []string{"O", "P", "S", "-", "4", "8", "2", "9", "backspace"} {
		a.editApprovalField(fields[1], key)
	}
	if got := a.model.approvalDraft["change_ticket"]; got != "OPS-482" {
		t.Fatalf("text draft = %q, want OPS-482", got)
	}

	// A boolean toggles on space and ignores text.
	a.editApprovalField(fields[2], " ")
	if urgent, _ := a.model.approvalVals["urgent"].(bool); !urgent {
		t.Fatal("space should toggle the boolean on")
	}

	values, err := a.collectApprovalValues(fields)
	if err != nil {
		t.Fatal(err)
	}
	if values["strategy"] != "full" || values["change_ticket"] != "OPS-482" || values["urgent"] != true {
		t.Fatalf("collected = %#v", values)
	}
}

// A required field left blank is caught before the round trip, and the message
// names the field rather than failing generically.
func TestApprovalFormRequiresDeclaredFields(t *testing.T) {
	a := newFormApp()
	a.openApprovalForm(fieldRequest())
	fields := approvalFields(a.model.approvalReq)

	_, err := a.collectApprovalValues(fields)
	if err == nil || !strings.Contains(err.Error(), "change_ticket") {
		t.Fatalf("expected a required-field error naming change_ticket, got %v", err)
	}

	// enter keeps the form open and shows the reason.
	a.model.approvalIdx = 1
	if _, cmd := a.handleApprovalFormKey("enter"); cmd != nil {
		t.Fatal("enter should not submit an incomplete form")
	}
	if !a.model.approvalActive {
		t.Fatal("an invalid submit should keep the form open")
	}
	if a.model.approvalErr == "" {
		t.Fatal("expected the validation message to be shown")
	}
}

// ctrl+r rejects from inside the form without collecting anything.
func TestApprovalFormRejectSkipsValidation(t *testing.T) {
	a := newFormApp()
	a.openApprovalForm(fieldRequest())
	_, cmd := a.handleApprovalFormKey("ctrl+r")
	if cmd == nil {
		t.Fatal("ctrl+r should post a rejection")
	}
	if a.model.approvalActive {
		t.Fatal("rejecting should close the form")
	}
}

func TestApprovalFormEscCancels(t *testing.T) {
	a := newFormApp()
	a.openApprovalForm(fieldRequest())
	if _, cmd := a.handleApprovalFormKey("esc"); cmd != nil {
		t.Fatal("esc should not send anything")
	}
	if a.model.approvalActive {
		t.Fatal("esc should close the form")
	}
}

// The form renders every option of a choice so the alternatives are visible
// without cycling through them.
func TestApprovalFormRendersOptions(t *testing.T) {
	a := newFormApp()
	a.openApprovalForm(fieldRequest())
	out := stripANSI(a.renderApprovalForm(""))
	for _, want := range []string{"Rollout strategy", "canary", "blue_green", "full", "approve", "reject"} {
		if !strings.Contains(out, want) {
			t.Fatalf("form is missing %q:\n%s", want, out)
		}
	}
}

// The banner must show the step's own question. Both call sites used to
// overwrite it with a fixed "reply on the task to resume or abort" line, which
// discarded the prompt and pointed operators at the source-comment flow — a
// no-op for a gate that declares no resume_on.
func TestApprovalPromptShowsTheStepsQuestion(t *testing.T) {
	item := &WorkflowInstanceItem{
		ID:    "wf-1",
		State: db.InstanceStateApprovalWaiting,
		Approval: &db.ApprovalRequest{
			ID:      "wf-1:gate",
			Message: "Release 2.4 is staged. How should it go out?",
		},
	}
	// A nil db keeps the already-loaded request; the message must win.
	applyApprovalPrompt(context.Background(), nil, item)
	if item.Message != "Release 2.4 is staged. How should it go out?" {
		t.Fatalf("banner = %q, want the approval's own message", item.Message)
	}
	if strings.Contains(item.Message, "reply on the task") {
		t.Fatal("the stale source-comment advice is back")
	}
}

// An empty Message hides the banner and its key hints, so a request without one
// still needs a non-empty fallback — but not the misleading old text.
func TestApprovalPromptFallsBackWithoutAMessage(t *testing.T) {
	item := &WorkflowInstanceItem{ID: "wf-1", State: db.InstanceStateApprovalWaiting}
	applyApprovalPrompt(context.Background(), nil, item)
	if item.Message == "" {
		t.Fatal("an empty message would hide the approval banner entirely")
	}
	if strings.Contains(item.Message, "reply on the task") {
		t.Fatalf("fallback should not advise replying on the task: %q", item.Message)
	}
}

// Instances that are not parked at an approval must be left alone.
func TestApprovalPromptIgnoresOtherStates(t *testing.T) {
	item := &WorkflowInstanceItem{ID: "wf-1", State: db.InstanceStateRunning, Message: "step 2 of 4"}
	applyApprovalPrompt(context.Background(), nil, item)
	if item.Message != "step 2 of 4" {
		t.Fatalf("message was rewritten for a running instance: %q", item.Message)
	}
}

// Overview must surface a waiting approval, since a gate with no timeout waits
// forever and nothing outside the Tasks tab used to mention one.
func TestOverviewShowsPendingApprovals(t *testing.T) {
	a := newFormApp()
	a.model.overviewTab = &OverviewTab{PendingApprovals: 2}
	out := stripANSI(a.renderOverviewTab(40))
	if !strings.Contains(out, "Approvals:") {
		t.Fatalf("overview is missing the approvals row:\n%s", out)
	}
	if !strings.Contains(out, "2 ⏸") {
		t.Fatalf("overview should show the pending count:\n%s", out)
	}

	// Zero stays quiet — it is a to-do counter, not an alert to cry wolf with.
	a.model.overviewTab = &OverviewTab{}
	out = stripANSI(a.renderOverviewTab(40))
	if !strings.Contains(out, "Approvals:") {
		t.Fatalf("the row should still render at zero:\n%s", out)
	}
	if strings.Contains(out, "⏸") {
		t.Fatalf("zero approvals should not render an alert:\n%s", out)
	}
}
