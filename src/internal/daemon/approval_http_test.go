package daemon

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
	d.handleApprovalResponse(w, req)
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
