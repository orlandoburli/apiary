package daemon

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
)

func TestApprovalWebhookSignature(t *testing.T) {
	body := []byte(`{"decision":"approve"}`)
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !validApprovalSignature("secret", body, sig) {
		t.Fatal("valid signature rejected")
	}
	if validApprovalSignature("wrong", body, sig) || validApprovalSignature("", body, sig) {
		t.Fatal("invalid signature accepted")
	}
}

func TestApprovalWebhookPersistsAuthorizedResponse(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "approvals.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbc.Close()
	request := &db.ApprovalRequest{ID: "inst:gate", WorkflowInstanceID: "inst", StepID: "gate", Approvers: []string{"alice"}}
	if err := dbc.CreateApprovalRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	d := &Dispatcher{db: dbc, cfg: &config.Config{Settings: config.Settings{Approvals: config.ApprovalSettings{WebhookSecret: "secret"}}}}
	body := []byte(`{"decision":"approve","actor":"alice","idempotency_key":"delivery-1"}`)
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write(body)
	req := httptest.NewRequest(http.MethodPost, "/approvals/inst:gate/webhook", bytes.NewReader(body))
	req.Header.Set("X-Apiary-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	w := httptest.NewRecorder()
	d.handleApprovalResponse(ctx, w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	stored, _ := dbc.GetApprovalRequest(ctx, request.ID)
	if stored.Status != db.ApprovalApproved || stored.RespondedBy != "alice" {
		t.Fatalf("stored=%+v", stored)
	}
}

func TestValidateApprovalResponseAuthorizationAndFields(t *testing.T) {
	req := &db.ApprovalRequest{Approvers: []string{"alice"}, Fields: []map[string]any{{"name": "ticket", "required": true, "type": "string"}}}
	good := db.ApprovalResponse{Decision: "approve", Actor: "alice", Values: map[string]any{"ticket": "OPS-7"}}
	if err := validateApprovalResponse(req, &good); err != nil {
		t.Fatal(err)
	}
	badActor := good
	badActor.Actor = "mallory"
	if err := validateApprovalResponse(req, &badActor); err == nil {
		t.Fatal("unauthorized actor accepted")
	}
	missing := good
	missing.Values = nil
	if err := validateApprovalResponse(req, &missing); err == nil {
		t.Fatal("missing required field accepted")
	}
}

func TestValidateApprovalResponseDelegationUsesApproverSlot(t *testing.T) {
	req := &db.ApprovalRequest{Approvers: []string{"alice"}, Delegates: map[string][]string{"alice": {"bob"}}}
	response := db.ApprovalResponse{Decision: "approve", Actor: "bob"}
	if err := validateApprovalResponse(req, &response); err != nil {
		t.Fatal(err)
	}
	if response.Approver != "alice" {
		t.Fatalf("approver slot=%q", response.Approver)
	}
}

// A rejection ends the gate, so it must not be blocked by the step's required
// fields — refusing a change should never mean filling in its change ticket.
func TestValidateApprovalResponseRejectSkipsRequiredFields(t *testing.T) {
	req := &db.ApprovalRequest{
		Fields: []map[string]any{{"name": "ticket", "required": true, "type": "string"}},
	}

	reject := db.ApprovalResponse{Decision: "reject", Actor: "orlando"}
	if err := validateApprovalResponse(req, &reject); err != nil {
		t.Fatalf("rejection should not require fields: %v", err)
	}

	// Approving the same gate still does.
	approve := db.ApprovalResponse{Decision: "approve", Actor: "orlando"}
	if err := validateApprovalResponse(req, &approve); err == nil {
		t.Fatal("approval without a required field should be rejected")
	}

	// Values that are supplied are still type-checked on a rejection.
	badType := db.ApprovalResponse{Decision: "reject", Actor: "orlando",
		Values: map[string]any{"ticket": 42.0}}
	badType.Values = map[string]any{"ticket": 42.0}
	req.Fields = []map[string]any{{"name": "ticket", "required": true, "type": "number"}}
	if err := validateApprovalResponse(req, &badType); err != nil {
		t.Fatalf("a well-typed value on a rejection should pass: %v", err)
	}
}

// An operator gate names no approvers, so whoever is at the keyboard can answer
// it — the actor is recorded as provenance, never checked.
func TestValidateApprovalResponseOperatorGateAcceptsAnyActor(t *testing.T) {
	req := &db.ApprovalRequest{ID: "wf:gate"}
	response := db.ApprovalResponse{Decision: "approve", Actor: "whoever"}
	if err := validateApprovalResponse(req, &response); err != nil {
		t.Fatalf("operator gate should accept any actor: %v", err)
	}
	if response.Approver != "" {
		t.Errorf("no approver slot should be assigned, got %q", response.Approver)
	}

	// An actor is still mandatory: the timeline must say who answered.
	anonymous := db.ApprovalResponse{Decision: "approve"}
	if err := validateApprovalResponse(req, &anonymous); err == nil {
		t.Error("expected a missing actor to be rejected")
	}
}

// Answering a gate that is already resolved is a conflict, not a silent no-op:
// the CLI maps 409 onto its "already answered" exit code.
func TestApprovalResponseOnResolvedRequestConflicts(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "approvals.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbc.Close()

	request := &db.ApprovalRequest{ID: "inst:gate", WorkflowInstanceID: "inst", StepID: "gate"}
	if err := dbc.CreateApprovalRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	d := &Dispatcher{db: dbc, cfg: &config.Config{}}

	post := func() *httptest.ResponseRecorder {
		body := []byte(`{"decision":"approve","actor":"orlando","idempotency_key":"cli:inst:gate:approve"}`)
		req := httptest.NewRequest(http.MethodPost, "/approvals/inst:gate/respond", bytes.NewReader(body))
		w := httptest.NewRecorder()
		d.handleApprovalResponse(ctx, w, req)
		return w
	}

	if w := post(); w.Code != http.StatusAccepted {
		t.Fatalf("first response: status=%d body=%s", w.Code, w.Body.String())
	}
	if w := post(); w.Code != http.StatusConflict {
		t.Fatalf("second response: status=%d body=%s, want 409", w.Code, w.Body.String())
	}
}

// The response body reports quorum progress so a client can say "1 of 2 recorded"
// instead of guessing from the status code alone.
func TestApprovalResponseReportsQuorumProgress(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "approvals.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbc.Close()

	request := &db.ApprovalRequest{ID: "inst:gate", WorkflowInstanceID: "inst", StepID: "gate",
		Approvers: []string{"alice", "bob"}, RequiredApprovals: 2}
	if err := dbc.CreateApprovalRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	d := &Dispatcher{db: dbc, cfg: &config.Config{}}

	body := []byte(`{"decision":"approve","actor":"alice","idempotency_key":"cli:inst:gate:alice"}`)
	req := httptest.NewRequest(http.MethodPost, "/approvals/inst:gate/respond", bytes.NewReader(body))
	w := httptest.NewRecorder()
	d.handleApprovalResponse(ctx, w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200 (recorded, not resolved)", w.Code, w.Body.String())
	}
	var out struct {
		Resolved  bool `json:"resolved"`
		Approvals int  `json:"approvals"`
		Required  int  `json:"required"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Resolved || out.Approvals != 1 || out.Required != 2 {
		t.Fatalf("progress = %+v, want 1 of 2 and unresolved", out)
	}
}
