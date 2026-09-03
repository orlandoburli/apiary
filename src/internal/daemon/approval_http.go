package daemon

import (
	"context"
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
	aplog "github.com/orlandoburli/apiary/internal/log"
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
	for i := range requests {
		d.enrichApprovalContext(r.Context(), &requests[i])
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(requests)
}

// enrichApprovalContext fills in TicketRef/TicketURL/PRNumber/PRURL from the
// request's TaskID, so an operator sees what ticket and PR they are answering
// for instead of just the internal workflow/step ids (#473).
//
// It is best-effort: a task with no source binding (or none yet) is common (an
// operator gate on a workflow with no source item) and is not an error, just an
// approval with no ticket context to show. It shares its resolution with
// `apiary approve`/`apiary approvals <id>`, both of which reach it indirectly —
// they fetch the same GET /approvals list and filter to the id they want, so a
// second resolution path was never needed.
func (d *Dispatcher) enrichApprovalContext(ctx context.Context, req *db.ApprovalRequest) {
	if req.TaskID == "" || d.db == nil {
		return
	}
	if bindings, err := d.db.ListBindingsByTask(ctx, req.TaskID); err == nil && len(bindings) > 0 {
		b := bindings[0]
		ref := b.SourceItemNumber
		if ref == "" {
			ref = b.SourceItemID
		}
		if ref != "" {
			req.TicketRef = b.SourceID + "/" + ref
		}
		req.TicketURL = b.SourceItemURL
	}
	if prs, err := d.db.ListTaskPullRequests(ctx, req.TaskID); err == nil && len(prs) > 0 {
		// The tail is the most recent PR (see ListTaskPullRequests).
		pr := prs[len(prs)-1]
		req.PRNumber = pr.PRNumber
		req.PRURL = pr.PRURL
	}
}

// handleApprovalResponse records one response to an approval request and, when
// that response resolves the gate, kicks the workflow advance off this goroutine.
//
// The split matters: ResolveApprovalResponse drives the whole graph, so a gate
// followed by a ten-minute agent step used to hold the HTTP response open for ten
// minutes. Every client gave up long before that — the dashboard's own client has
// a 5s timeout — so a successful approval read as a failure. The response is
// durable the moment it is persisted; the advance is a consequence, not part of
// the answer, and checkApprovals would eventually drive it anyway.
//
// ctx is the daemon's lifetime context, NOT the request's: the advance outlives
// the HTTP exchange that triggered it and must not be cancelled when it returns.
func (d *Dispatcher) handleApprovalResponse(ctx context.Context, w http.ResponseWriter, r *http.Request) {
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
	var response db.ApprovalResponse
	if err := json.Unmarshal(body, &response); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	// The channel is recorded in the audit timeline, so it must say where the
	// answer came from. On /respond the local client names itself — dashboard or
	// cli — and anything else is normalized away rather than trusted verbatim.
	// On /webhook it is forced, because that label carries the signature check.
	if parts[1] == "webhook" {
		if !validApprovalSignature(d.cfg.Settings.Approvals.WebhookSecret, body, r.Header.Get("X-Apiary-Signature")) {
			http.Error(w, "invalid webhook signature", http.StatusUnauthorized)
			return
		}
		response.Channel = "webhook"
	} else if response.Channel != "cli" {
		response.Channel = "dashboard"
	}
	if response.IdempotencyKey == "" {
		response.IdempotencyKey = r.Header.Get("Idempotency-Key")
	}
	request, err := d.db.GetApprovalRequest(ctx, parts[0])
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if request == nil {
		http.Error(w, "approval request not found", http.StatusNotFound)
		return
	}
	if request.Status != db.ApprovalPending && request.Status != db.ApprovalEscalated {
		http.Error(w, fmt.Sprintf("approval request %s is already %s", request.ID, request.Status), http.StatusConflict)
		return
	}
	if err := validateApprovalResponse(request, &response); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	stored, won, err := d.db.ResolveApprovalRequest(ctx, request.ID, response)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Reply before advancing. The caller learns its answer was recorded and, when
	// the gate is now resolved, that the workflow is resuming — not that the
	// resumed workflow has finished.
	approvals, _ := d.db.CountApprovals(ctx, request.ID)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(map[bool]int{true: http.StatusAccepted, false: http.StatusOK}[won])
	_ = json.NewEncoder(w).Encode(map[string]any{
		"recorded":  true,
		"resolved":  won,
		"approvals": approvals,
		"required":  request.RequiredApprovals,
		"request":   stored,
	})
	if won {
		if d.engine == nil {
			d.workflowEngine()
		}
		d.advanceParkedApproval(ctx, request.WorkflowInstanceID, response)
	}
}

// advanceParkedApproval drives the graph advance for an instance whose approval
// request has just been resolved, on a background goroutine.
//
// It shares checkApprovals' two guards so the two paths can never double-advance
// one instance: approvalAdvancing claims it (the next poll cycle skips a claimed
// instance), and the advance is admitted through the resolved instance's agent
// semaphore so a follow-on agent step still respects max_workers.
//
// Losing the claim is not an error — it means the poll loop is already advancing
// this instance from the same persisted response, which is the identical outcome.
func (d *Dispatcher) advanceParkedApproval(ctx context.Context, instanceID string, response db.ApprovalResponse) {
	if d.engine == nil || instanceID == "" {
		return
	}
	if _, busy := d.approvalAdvancing.LoadOrStore(instanceID, struct{}{}); busy {
		return
	}
	agentCh := d.agentSem[d.parkedApprovalAgent(instanceID)]
	d.goBackground(func() {
		defer d.approvalAdvancing.Delete(instanceID)
		if agentCh != nil {
			select {
			case agentCh <- struct{}{}:
			case <-ctx.Done():
				return // shutting down before a slot freed; never acquired
			}
			defer func() { <-agentCh }()
		}
		if _, err := d.engine.ResolveApprovalResponse(ctx, instanceID, response); err != nil {
			// The response is already persisted, so this is recoverable: the next
			// poll cycle re-reads it from the store and retries the advance.
			aplog.Warn("approval: advancing instance %s after response: %v", instanceID, err)
		}
	})
}

// parkedApprovalAgent returns the representative agent id of the instance parked
// at instanceID, or "" when it is not parked at an approval (already advanced, or
// never was). An empty id gates as ungated, matching checkApprovals.
func (d *Dispatcher) parkedApprovalAgent(instanceID string) string {
	for _, p := range d.engine.ParkedApprovals() {
		if p.InstanceID == instanceID {
			return p.AgentID
		}
	}
	return ""
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
	// A rejection ends the gate, so its form is moot: demanding a change ticket
	// from someone who is refusing the change is busywork, and it blocked the only
	// answer a reviewer can give quickly. Values that ARE supplied are still typed.
	rejecting := decision == "reject" || decision == "rejected"
	for _, field := range request.Fields {
		name, _ := field["name"].(string)
		required, _ := field["required"].(bool)
		value, present := response.Values[name]
		if required && !rejecting && (!present || value == nil || value == "") {
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
