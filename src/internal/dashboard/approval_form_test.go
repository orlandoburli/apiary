package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/state"
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

// y and n used to answer in one keystroke. They now only open the sheet with an
// outcome preselected, so there is always somewhere to type a reason; a opens it
// on the first declared outcome without expressing a preference.
func TestAnswerApprovalOpensTheSheet(t *testing.T) {
	cases := []struct{ key, want string }{
		{"y", "approve"},
		{"n", "reject"},
		{"a", "approve"},
	}
	for _, tc := range cases {
		a := newFormApp()
		if _, cmd := a.answerApproval(&db.ApprovalRequest{ID: "wf:gate"}, tc.key); cmd != nil {
			t.Fatalf("%q answered immediately; it should open the sheet", tc.key)
		}
		if !a.model.approvalActive {
			t.Fatalf("%q did not open the sheet", tc.key)
		}
		if got := a.model.approvalOutcomes[a.model.approvalOutcome].Value; got != tc.want {
			t.Fatalf("%q preselected %q, want %q", tc.key, got, tc.want)
		}
	}

	// The same keys work on a gate with fields; the fields are seeded so the
	// operator can answer in one pass.
	a := newFormApp()
	if _, cmd := a.answerApproval(fieldRequest(), "y"); cmd != nil {
		t.Fatal("y on a gate with fields should open the sheet, not post")
	}
	if a.model.approvalVals["strategy"] != "canary" {
		t.Fatal("declared fields were not seeded")
	}
}

// A workflow may name its answers freely; y/n still land on the nearest thing to
// approve/reject, by the outcome's own Fail flag.
func TestAnswerApprovalPreselectsCustomOutcomes(t *testing.T) {
	req := &db.ApprovalRequest{
		ID: "wf:gate",
		Outcomes: []db.ApprovalOutcome{
			{Value: "ship", Label: "Ship it"},
			{Value: "hold", Label: "Hold", Fail: true},
		},
	}
	a := newFormApp()
	a.answerApproval(req, "n")
	if got := a.model.approvalOutcomes[a.model.approvalOutcome].Value; got != "hold" {
		t.Fatalf("n preselected %q, want hold", got)
	}
	a = newFormApp()
	a.answerApproval(req, "y")
	if got := a.model.approvalOutcomes[a.model.approvalOutcome].Value; got != "ship" {
		t.Fatalf("y preselected %q, want ship", got)
	}
	a = newFormApp()
	a.answerApproval(req, "a")
	if got := a.model.approvalOutcomes[a.model.approvalOutcome].Value; got != "ship" {
		t.Fatalf("a should preselect the first outcome, got %q", got)
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

	// enter submits from a field row, and keeps the sheet open when invalid.
	a.model.approvalFocus = approvalFocusFields
	a.model.approvalIdx = 1
	if _, cmd := a.handleApprovalFormKey("enter"); cmd != nil {
		t.Fatal("enter should not submit an incomplete form")
	}
	if !a.model.approvalActive {
		t.Fatal("an invalid submit should keep the sheet open")
	}
	if a.model.approvalErr == "" {
		t.Fatal("expected the validation message to be shown")
	}
}

// A failing outcome ends the gate, so its fields are never validated or sent —
// the same rule a direct rejection always had.
func TestApprovalSheetFailOutcomeSkipsValidation(t *testing.T) {
	req := fieldRequest()
	req.Outcomes = []db.ApprovalOutcome{
		{Value: "ship"},
		{Value: "hold", Label: "Hold the release", Fail: true},
	}
	a := newFormApp()
	a.openApprovalFormWith(req, "reject")
	if got := a.model.approvalOutcomes[a.model.approvalOutcome].Value; got != "hold" {
		t.Fatalf("reject preselected %q, want hold", got)
	}

	// The required change_ticket is blank, yet a failing answer submits.
	_, cmd := a.handleApprovalFormKey("ctrl+s")
	if cmd == nil {
		t.Fatal("a fail outcome should submit without filling in the fields")
	}
	if a.model.approvalActive {
		t.Fatal("submitting should close the sheet")
	}
	if a.model.approvalErr != "" {
		t.Fatalf("a failing answer should not validate fields, got %q", a.model.approvalErr)
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

// The sheet renders every choice option, both outcomes, and the always-present
// reason row, so none of the answers is hidden behind a cycle.
func TestApprovalFormRendersOptions(t *testing.T) {
	a := newFormApp()
	a.openApprovalForm(fieldRequest())
	out := stripANSI(a.renderApprovalForm(""))
	for _, want := range []string{
		"Rollout strategy", "canary", "blue_green", "full",
		"Outcome", "approve", "reject", "Reason (optional)", "▶", "●",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("sheet is missing %q:\n%s", want, out)
		}
	}
}

// The context line names what is being answered without hunting in the task.
func TestApprovalSheetShowsContext(t *testing.T) {
	a := newFormApp()
	req := fieldRequest()
	req.StepID = "gate"
	req.TicketRef = "OPS-482"
	req.PRNumber = 91
	a.openApprovalForm(req)

	out := stripANSI(a.renderApprovalForm(""))
	for _, want := range []string{"wf-1:gate", "step gate", "ticket OPS-482", "PR #91"} {
		if !strings.Contains(out, want) {
			t.Fatalf("context is missing %q:\n%s", want, out)
		}
	}
}

// ↑/↓ move the ▶ cursor without deciding; enter turns the cursor into the
// selection and must not submit. Digits and ←/→ select directly.
func TestApprovalSheetOutcomeSelection(t *testing.T) {
	req := &db.ApprovalRequest{
		ID: "wf:gate",
		Outcomes: []db.ApprovalOutcome{
			{Value: "ship", Label: "Ship it"},
			{Value: "hold", Label: "Hold", Fail: true},
			{Value: "kill", Label: "Kill", Fail: true},
		},
	}
	a := newFormApp()
	a.openApprovalFormWith(req, "reject")
	if a.model.approvalOutcome != 1 {
		t.Fatalf("selected = %d, want 1 (hold)", a.model.approvalOutcome)
	}

	if _, cmd := a.handleApprovalFormKey("down"); cmd != nil {
		t.Fatal("down on an outcome must not submit")
	}
	if a.model.approvalCursor != 2 || a.model.approvalOutcome != 1 {
		t.Fatalf("cursor=%d selected=%d; down should move only the cursor",
			a.model.approvalCursor, a.model.approvalOutcome)
	}
	if _, cmd := a.handleApprovalFormKey("enter"); cmd != nil {
		t.Fatal("enter on an outcome selects it; it must not submit")
	}
	if a.model.approvalOutcome != 2 {
		t.Fatalf("enter selected %d, want 2", a.model.approvalOutcome)
	}

	// A digit jumps and selects.
	a.handleApprovalFormKey("1")
	if a.model.approvalOutcome != 0 || a.model.approvalCursor != 0 {
		t.Fatalf("1 selected %d (cursor %d), want 0", a.model.approvalOutcome, a.model.approvalCursor)
	}
	// ←/→ move the selection and bring the cursor along.
	a.handleApprovalFormKey("right")
	if a.model.approvalOutcome != 1 || a.model.approvalCursor != 1 {
		t.Fatalf("right selected %d (cursor %d), want 1", a.model.approvalOutcome, a.model.approvalCursor)
	}
}

// Tab cycles outcomes → fields → reason and back, skipping the fields section
// when the gate declares none.
func TestApprovalSheetTabCyclesSections(t *testing.T) {
	a := newFormApp()
	a.openApprovalForm(fieldRequest())
	if a.model.approvalFocus != approvalFocusOutcome {
		t.Fatal("the sheet should open on the outcomes")
	}
	a.handleApprovalFormKey("tab")
	if a.model.approvalFocus != approvalFocusFields {
		t.Fatal("tab should move to the fields")
	}
	a.handleApprovalFormKey("tab")
	if a.model.approvalFocus != approvalFocusReason {
		t.Fatal("tab should move to the reason")
	}
	a.handleApprovalFormKey("tab")
	if a.model.approvalFocus != approvalFocusOutcome {
		t.Fatal("tab should wrap back to the outcomes")
	}

	a.openApprovalForm(&db.ApprovalRequest{ID: "wf:gate"})
	a.handleApprovalFormKey("tab")
	if a.model.approvalFocus != approvalFocusReason {
		t.Fatal("with no fields, tab should go straight to the reason")
	}
}

// The banner must show the step's own question. Both call sites used to
// overwrite it with a fixed "reply on the task to resume or abort" line, which
// discarded the prompt and pointed operators at the source-comment flow — a
// no-op for a gate that declares no resume_on.
func TestApprovalPromptShowsTheStepsQuestion(t *testing.T) {
	item := &WorkflowInstanceItem{
		ID:    "wf-1",
		State: db.InstanceStateBlocked, BlockedReason: string(state.ReasonApproval),
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
	item := &WorkflowInstanceItem{ID: "wf-1", State: db.InstanceStateBlocked, BlockedReason: string(state.ReasonApproval)}
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

// The gate's message is markdown, and the form must keep its shape. It used to
// be word-wrapped into one yellow paragraph, which collapsed every line break,
// list item and code block into prose.
func TestApprovalFormRendersMessageAsMarkdown(t *testing.T) {
	a := newFormApp()
	a.model.width, a.model.height = 100, 40
	req := fieldRequest()
	req.Message = "Release 2.4 is staged.\n\n- migrations applied\n- smoke tests green\n\nProceed?"
	a.openApprovalForm(req)

	out := stripANSI(a.renderApprovalForm(""))
	for _, want := range []string{"migrations applied", "smoke tests green", "Proceed?"} {
		if !strings.Contains(out, want) {
			t.Fatalf("form is missing %q:\n%s", want, out)
		}
	}
	// The two bullets are separate lines, not one run-together sentence.
	if strings.Contains(out, "migrations applied smoke tests green") {
		t.Fatalf("list items were collapsed into a paragraph:\n%s", out)
	}
}

// A message longer than the terminal must not push the fields and key hints off
// the screen — the operator would have no way left to answer the gate.
func TestApprovalFormTruncatesLongMessage(t *testing.T) {
	a := newFormApp()
	a.model.width, a.model.height = 100, 32
	req := fieldRequest()
	req.Message = strings.Repeat("- a bullet\n", 40)
	a.openApprovalForm(req)

	out := stripANSI(a.renderApprovalForm(""))
	if !strings.Contains(out, "more lines") {
		t.Fatalf("long message should be trimmed with a hint:\n%s", out)
	}
	if !strings.Contains(out, "approve") || !strings.Contains(out, "Rollout strategy") ||
		!strings.Contains(out, "Reason (optional)") {
		t.Fatalf("outcomes, fields and key hints must survive a long message:\n%s", out)
	}
	if got := lipgloss.Height(a.renderApprovalForm("")); got > a.model.height {
		t.Fatalf("dialog is %d lines tall, taller than the %d-line terminal", got, a.model.height)
	}
}

// The form's text fields must win over the global single-letter bindings. They
// used to be checked after them, so typing a note ran q (quit the dashboard),
// o (open the task in a browser), t or p instead of typing the character.
func TestApprovalFormOwnsGlobalLetterKeys(t *testing.T) {
	a := &App{model: &Model{width: 100, height: 40, tabs: []string{"Overview", "Tasks"}, activeTab: 1}}
	a.openApprovalForm(fieldRequest())
	a.model.approvalFocus = approvalFocusFields
	a.model.approvalIdx = 1 // the string field

	for _, key := range []string{"o", "k", "q", "t", "p"} {
		if _, cmd := a.handleKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}); cmd != nil {
			t.Fatalf("%q fired a global binding while the form was open", key)
		}
		if !a.model.approvalActive {
			t.Fatalf("%q closed the form", key)
		}
	}
	if got := a.model.approvalDraft["change_ticket"]; got != "okqtp" {
		t.Fatalf("field got %q, want %q", got, "okqtp")
	}
}

// startApprovalSocket stands in for the daemon's Unix socket, capturing the JSON
// the dashboard posts to /respond.
func startApprovalSocket(t *testing.T) (string, <-chan map[string]any) {
	t.Helper()
	dir, err := os.MkdirTemp("", "apiary-approval")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "apiary.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	captured := make(chan map[string]any, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		captured <- payload
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"resolved":true,"approvals":1,"required":1}`)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = os.RemoveAll(dir)
	})
	return sock, captured
}

// The whole sheet round-trips: the selected outcome, the typed reason, and the
// declared field values reach the daemon in one response.
func TestApprovalSheetSubmitsOutcomeReasonAndValues(t *testing.T) {
	sock, captured := startApprovalSocket(t)
	a := newFormApp()
	a.socketPath = sock
	a.openApprovalFormWith(fieldRequest(), "approve")

	a.model.approvalVals["strategy"] = "blue_green"
	a.model.approvalDraft["change_ticket"] = "OPS-482"

	// Tab to the reason and type it, including a space.
	a.handleApprovalFormKey("tab") // fields
	a.handleApprovalFormKey("tab") // reason
	if a.model.approvalFocus != approvalFocusReason {
		t.Fatal("tab did not reach the reason")
	}
	for _, key := range []string{"n", "e", "e", "d", "s", " ", "l", "o", "o", "k"} {
		a.handleApprovalFormKey(key)
	}
	if got := a.model.approvalReason; got != "needs look" {
		t.Fatalf("reason = %q, want %q", got, "needs look")
	}

	_, cmd := a.handleApprovalFormKey("ctrl+s")
	if cmd == nil {
		t.Fatal("ctrl+s should submit")
	}
	cmd() // the request lands on the socket synchronously

	payload := <-captured
	if payload["decision"] != "approve" {
		t.Fatalf("decision = %v, want approve", payload["decision"])
	}
	if payload["outcome"] != "approve" {
		t.Fatalf("outcome = %v, want approve", payload["outcome"])
	}
	if payload["feedback"] != "needs look" {
		t.Fatalf("feedback = %v, want the typed reason", payload["feedback"])
	}
	values, _ := payload["values"].(map[string]any)
	if values["strategy"] != "blue_green" || values["change_ticket"] != "OPS-482" {
		t.Fatalf("values = %#v", payload["values"])
	}
}

// A declared outcome that fails submits decision=reject, sends its own value,
// and carries the reason — without asking for fields.
func TestApprovalSheetFailOutcomeSubmitsReject(t *testing.T) {
	sock, captured := startApprovalSocket(t)
	req := fieldRequest()
	req.Outcomes = []db.ApprovalOutcome{
		{Value: "ship"},
		{Value: "hold", Label: "Hold", Fail: true},
	}
	a := newFormApp()
	a.socketPath = sock
	a.openApprovalFormWith(req, "reject")

	a.handleApprovalFormKey("tab") // fields
	a.handleApprovalFormKey("tab") // reason
	for _, key := range []string{"n", "o", "t", " ", "y", "e", "t"} {
		a.handleApprovalFormKey(key)
	}
	_, cmd := a.handleApprovalFormKey("ctrl+s")
	if cmd == nil {
		t.Fatal("ctrl+s should submit a fail outcome")
	}
	cmd()

	payload := <-captured
	if payload["decision"] != "reject" {
		t.Fatalf("decision = %v, want reject", payload["decision"])
	}
	if payload["outcome"] != "hold" {
		t.Fatalf("outcome = %v, want hold", payload["outcome"])
	}
	if payload["feedback"] != "not yet" {
		t.Fatalf("feedback = %v, want not yet", payload["feedback"])
	}
	if _, ok := payload["values"]; ok {
		t.Fatalf("a failing outcome must not send field values: %#v", payload["values"])
	}
}

// Enter inside the reason is a submit gesture; esc on the sheet records nothing.
func TestApprovalSheetReasonEnterSubmits(t *testing.T) {
	a := newFormApp()
	a.openApprovalForm(&db.ApprovalRequest{ID: "wf:gate"})
	a.model.approvalFocus = approvalFocusReason
	if _, cmd := a.handleApprovalFormKey("enter"); cmd == nil {
		t.Fatal("enter in the reason should submit")
	}

	a = newFormApp()
	a.openApprovalForm(fieldRequest())
	if _, cmd := a.handleApprovalFormKey("esc"); cmd != nil {
		t.Fatal("esc must not send a response")
	}
	if a.model.approvalActive {
		t.Fatal("esc should close the sheet")
	}
}
