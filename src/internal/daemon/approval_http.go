package daemon

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/orlandoburli/apiary/internal/db"
)

func (d *Dispatcher) handleApprovals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if d.db == nil {
		http.Error(w, "approval store unavailable", http.StatusServiceUnavailable)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	requests, err := d.db.ListApprovalRequests(r.Context(), r.URL.Query().Get("status"), limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(requests)
}

func (d *Dispatcher) handleApprovalResponse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if d.db == nil {
		http.Error(w, "approval store unavailable", http.StatusServiceUnavailable)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/approvals/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || (parts[1] != "respond" && parts[1] != "webhook") {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	channel := "dashboard"
	if parts[1] == "webhook" {
		channel = "webhook"
		if !validApprovalSignature(d.cfg.Settings.Approvals.WebhookSecret, body, r.Header.Get("X-Apiary-Signature")) {
			http.Error(w, "invalid webhook signature", http.StatusUnauthorized)
			return
		}
	} else {
		// Dashboard (/respond) path: require the control token so that only
		// authenticated local processes can approve or reject gates. This closes
		// the bypass described in SEC-11 where an unauthenticated local caller
		// could approve gates without the signature check the webhook path enforces.
		if d.controlToken == "" || r.Header.Get("X-Apiary-Control") != d.controlToken {
			http.Error(w, "unauthorized: control token required", http.StatusUnauthorized)
			return
		}
	}
	var response db.ApprovalResponse
	if err := json.Unmarshal(body, &response); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	response.Channel = channel
	if response.IdempotencyKey == "" {
		response.IdempotencyKey = r.Header.Get("Idempotency-Key")
	}
	request, err := d.db.GetApprovalRequest(r.Context(), parts[0])
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if request == nil {
		http.Error(w, "approval request not found", http.StatusNotFound)
		return
	}
	if err := validateApprovalResponse(request, &response); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	stored, won, err := d.db.ResolveApprovalRequest(r.Context(), request.ID, response)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if won {
		if d.engine == nil {
			d.workflowEngine()
		}
		if d.engine != nil {
			_, _ = d.engine.ResolveApprovalResponse(r.Context(), request.WorkflowInstanceID, response)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(map[bool]int{true: http.StatusAccepted, false: http.StatusOK}[won])
	_ = json.NewEncoder(w).Encode(map[string]any{"recorded": true, "resolved": won, "request": stored})
}

func validApprovalSignature(secret string, body []byte, header string) bool {
	if secret == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	want := mac.Sum(nil)
	header = strings.TrimPrefix(header, "sha256=")
	got, err := hex.DecodeString(header)
	return err == nil && hmac.Equal(want, got)
}

func validateApprovalResponse(request *db.ApprovalRequest, response *db.ApprovalResponse) error {
	decision := strings.ToLower(response.Decision)
	if decision != "approve" && decision != "approved" && decision != "reject" && decision != "rejected" {
		return fmt.Errorf("decision must be approve or reject")
	}
	if response.Actor == "" {
		return fmt.Errorf("actor is required")
	}
	if len(request.Approvers) > 0 {
		principal := ""
		for _, actor := range request.Approvers {
			if strings.EqualFold(actor, response.Actor) {
				principal = actor
				break
			}
		}
		if principal == "" {
			for approver, delegates := range request.Delegates {
				for _, delegate := range delegates {
					if strings.EqualFold(delegate, response.Actor) && (response.Approver == "" || strings.EqualFold(response.Approver, approver)) {
						principal = approver
					}
				}
			}
		}
		if principal == "" {
			return fmt.Errorf("actor %q is not an authorized approver", response.Actor)
		}
		response.Approver = principal
	}
	for _, field := range request.Fields {
		name, _ := field["name"].(string)
		required, _ := field["required"].(bool)
		value, present := response.Values[name]
		if required && (!present || value == nil || value == "") {
			return fmt.Errorf("field %q is required", name)
		}
		if !present {
			continue
		}
		typeName, _ := field["type"].(string)
		switch typeName {
		case "boolean":
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("field %q must be boolean", name)
			}
		case "number":
			if _, ok := value.(float64); !ok {
				return fmt.Errorf("field %q must be number", name)
			}
		case "choice":
			allowed := false
			if options, ok := field["options"].([]any); ok {
				for _, option := range options {
					if option == value {
						allowed = true
					}
				}
			}
			if !allowed {
				return fmt.Errorf("field %q is not an allowed choice", name)
			}
		}
	}
	return nil
}
