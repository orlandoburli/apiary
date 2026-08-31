package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	ApprovalPending   = "pending"
	ApprovalEscalated = "escalated"
	ApprovalApproved  = "approved"
	ApprovalRejected  = "rejected"
	ApprovalTimedOut  = "timed_out"
)

type ApprovalRequest struct {
	ID                 string              `json:"id"`
	WorkflowInstanceID string              `json:"workflow_instance_id"`
	TaskID             string              `json:"task_id,omitempty"`
	WorkflowID         string              `json:"workflow_id,omitempty"`
	StepID             string              `json:"step_id"`
	Message            string              `json:"message,omitempty"`
	Approvers          []string            `json:"approvers,omitempty"`
	Delegates          map[string][]string `json:"delegates,omitempty"`
	RequiredApprovals  int                 `json:"required_approvals"`
	Fields             []map[string]any    `json:"fields,omitempty"`
	Status             string              `json:"status"`
	Values             map[string]any      `json:"values,omitempty"`
	Feedback           string              `json:"feedback,omitempty"`
	RespondedBy        string              `json:"responded_by,omitempty"`
	ResponseChannel    string              `json:"response_channel,omitempty"`
	IdempotencyKey     string              `json:"idempotency_key,omitempty"`
	// Attempt counts this step's visits within the instance, starting at 1. A
	// rework loop re-enters the same approval step on the same instance and each
	// lap gets its own answerable request (#462).
	Attempt     int        `json:"attempt,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	RemindedAt  *time.Time `json:"reminded_at,omitempty"`
	EscalatedAt *time.Time `json:"escalated_at,omitempty"`
	RespondedAt *time.Time `json:"responded_at,omitempty"`
}

type ApprovalResponse struct {
	Decision       string         `json:"decision"`
	Actor          string         `json:"actor"`
	Approver       string         `json:"for_approver,omitempty"`
	Channel        string         `json:"channel"`
	IdempotencyKey string         `json:"idempotency_key"`
	Feedback       string         `json:"feedback,omitempty"`
	Values         map[string]any `json:"values,omitempty"`
}

// CreateApprovalRequest persists a request for one visit to an approval step.
//
// The caller supplies a base id (instance:step). When the step is re-entered by
// a rework loop the previous lap's request is already terminally resolved, so a
// fresh attempt is minted and the id is suffixed "@<attempt>" to keep it unique
// — req.ID is rewritten in place, and callers must use it (not the id they
// passed) for notifications and audit events. Reusing lap 1's resolved row
// instead is what wedged looping workflows: every later lap dead-ended at an
// approval that answered "already approved" (#462).
//
// It is still idempotent within a lap: while a request for the pair is
// unresolved (pending or escalated), re-creating returns that row verbatim —
// req is overwritten with the stored one — rather than opening a second gate.
func (c *Client) CreateApprovalRequest(ctx context.Context, req *ApprovalRequest) error {
	if req.ID == "" || req.WorkflowInstanceID == "" || req.StepID == "" {
		return fmt.Errorf("approval id, instance, and step are required")
	}
	if req.RequiredApprovals <= 0 {
		req.RequiredApprovals = 1
	}
	if req.Status == "" {
		req.Status = ApprovalPending
	}
	if req.CreatedAt.IsZero() {
		req.CreatedAt = time.Now().UTC()
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// An unresolved request for this step is the gate that is already open:
	// hand it back instead of opening a second one.
	open, err := c.scanApproval(tx.QueryRowContext(ctx, approvalSelect+
		` WHERE workflow_instance_id=? AND step_id=? AND status IN (?,?) ORDER BY attempt DESC LIMIT 1`,
		req.WorkflowInstanceID, req.StepID, ApprovalPending, ApprovalEscalated))
	if err != nil {
		return err
	}
	if open != nil {
		*req = *open
		return nil
	}

	if req.Attempt <= 0 {
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(attempt), 0) + 1 FROM approval_requests WHERE workflow_instance_id=? AND step_id=?`,
			req.WorkflowInstanceID, req.StepID).Scan(&req.Attempt); err != nil {
			return err
		}
	}
	if req.Attempt > 1 {
		req.ID = fmt.Sprintf("%s@%d", req.ID, req.Attempt)
	}

	approvers, _ := json.Marshal(req.Approvers)
	delegates, _ := json.Marshal(req.Delegates)
	fields, _ := json.Marshal(req.Fields)
	if _, err := tx.ExecContext(ctx, `INSERT INTO approval_requests
		(id, workflow_instance_id, task_id, workflow_id, step_id, message, approvers, delegates, required_approvals, fields, status, created_at, expires_at, attempt)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(workflow_instance_id, step_id, attempt) DO NOTHING`,
		req.ID, req.WorkflowInstanceID, nullStr(req.TaskID), nullStr(req.WorkflowID), req.StepID, nullStr(req.Message), string(approvers), string(delegates), req.RequiredApprovals, string(fields), req.Status, req.CreatedAt, req.ExpiresAt, req.Attempt); err != nil {
		return err
	}
	return tx.Commit()
}

func (c *Client) GetApprovalRequest(ctx context.Context, id string) (*ApprovalRequest, error) {
	return c.scanApproval(c.db.QueryRowContext(ctx, approvalSelect+` WHERE id=?`, id))
}

// GetApprovalByInstance returns the instance's most recently minted request —
// the one it is parked on. rowid breaks a created_at tie so a loop that re-enters
// the same step twice inside one clock tick still returns the newer lap's row and
// not the resolved one it supersedes (#462).
func (c *Client) GetApprovalByInstance(ctx context.Context, id string) (*ApprovalRequest, error) {
	return c.scanApproval(c.db.QueryRowContext(ctx, approvalSelect+` WHERE workflow_instance_id=? ORDER BY created_at DESC, rowid DESC LIMIT 1`, id))
}
func (c *Client) ListApprovalRequests(ctx context.Context, status string, limit int) ([]ApprovalRequest, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := approvalSelect
	args := []any{}
	if status != "" {
		query += ` WHERE status=?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ApprovalRequest
	for rows.Next() {
		req, err := scanApprovalRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *req)
	}
	return out, rows.Err()
}

func (c *Client) ResolveApprovalRequest(ctx context.Context, id string, response ApprovalResponse) (*ApprovalRequest, bool, error) {
	status := ApprovalApproved
	if strings.EqualFold(response.Decision, "reject") || strings.EqualFold(response.Decision, "rejected") {
		status = ApprovalRejected
	}
	values, err := json.Marshal(response.Values)
	if err != nil {
		return nil, false, err
	}
	if response.IdempotencyKey == "" {
		return nil, false, fmt.Errorf("idempotency key is required")
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	var current string
	var required int
	if err := tx.QueryRowContext(ctx, `SELECT status, required_approvals FROM approval_requests WHERE id=?`, id).Scan(&current, &required); err != nil {
		return nil, false, err
	}
	if current != ApprovalPending && current != ApprovalEscalated {
		tx.Rollback()
		req, getErr := c.GetApprovalRequest(ctx, id)
		return req, false, getErr
	}
	now := time.Now().UTC()
	approver := response.Approver
	if approver == "" {
		approver = response.Actor
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO approval_responses (request_id, decision, actor, approver, channel, idempotency_key, feedback, values_json, created_at) VALUES (?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, id, status, response.Actor, approver, response.Channel, response.IdempotencyKey, nullStr(response.Feedback), string(values), now)
	if err != nil {
		return nil, false, err
	}
	inserted, _ := res.RowsAffected()
	if inserted == 0 {
		tx.Rollback()
		req, getErr := c.GetApprovalRequest(ctx, id)
		return req, false, getErr
	}
	resolved := status == ApprovalRejected
	if !resolved {
		var approvals int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM approval_responses WHERE request_id=? AND decision=?`, id, ApprovalApproved).Scan(&approvals); err != nil {
			return nil, false, err
		}
		resolved = approvals >= required
	}
	if resolved {
		if _, err := tx.ExecContext(ctx, `UPDATE approval_requests SET status=?, response_values=?, feedback=?, responded_by=?, response_channel=?, idempotency_key=?, responded_at=? WHERE id=? AND status IN (?,?)`, status, string(values), nullStr(response.Feedback), response.Actor, response.Channel, response.IdempotencyKey, now, id, ApprovalPending, ApprovalEscalated); err != nil {
			return nil, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	req, getErr := c.GetApprovalRequest(ctx, id)
	return req, resolved, getErr
}

// CountPendingApprovals returns how many approval requests are still unanswered.
// Escalated counts as unanswered: escalation re-targets a request, it does not
// resolve it — an operator still has to respond.
func (c *Client) CountPendingApprovals(ctx context.Context) (int, error) {
	var n int
	err := c.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM approval_requests WHERE status IN (?,?)`,
		ApprovalPending, ApprovalEscalated).Scan(&n)
	return n, err
}

// CountApprovals returns how many approve responses a request has recorded so
// far, for progress display against RequiredApprovals. A request with no
// approvers resolves on the first response, so this is only interesting for a
// multi-party gate.
func (c *Client) CountApprovals(ctx context.Context, id string) (int, error) {
	var n int
	err := c.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM approval_responses WHERE request_id=? AND decision=?`,
		id, ApprovalApproved).Scan(&n)
	return n, err
}

func (c *Client) MarkApprovalTimedOut(ctx context.Context, id string) (bool, error) {
	return c.updateApprovalStatus(ctx, id, ApprovalTimedOut, ApprovalPending, ApprovalEscalated)
}
func (c *Client) MarkApprovalReminded(ctx context.Context, id string) (bool, error) {
	res, err := c.db.ExecContext(ctx, `UPDATE approval_requests SET reminded_at=? WHERE id=? AND status IN (?,?) AND reminded_at IS NULL`, time.Now().UTC(), id, ApprovalPending, ApprovalEscalated)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
func (c *Client) EscalateApproval(ctx context.Context, id string) (bool, error) {
	res, err := c.db.ExecContext(ctx, `UPDATE approval_requests SET status=?, escalated_at=? WHERE id=? AND status=?`, ApprovalEscalated, time.Now().UTC(), id, ApprovalPending)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
func (c *Client) updateApprovalStatus(ctx context.Context, id, status string, from ...string) (bool, error) {
	placeholders := strings.TrimRight(strings.Repeat("?,", len(from)), ",")
	args := []any{status, time.Now().UTC(), id}
	for _, s := range from {
		args = append(args, s)
	}
	res, err := c.db.ExecContext(ctx, `UPDATE approval_requests SET status=?, responded_at=? WHERE id=? AND status IN (`+placeholders+`)`, args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

const approvalSelect = `SELECT id, workflow_instance_id, COALESCE(task_id,''), COALESCE(workflow_id,''), step_id, COALESCE(message,''), approvers, delegates, required_approvals, fields, status, response_values, COALESCE(feedback,''), COALESCE(responded_by,''), COALESCE(response_channel,''), COALESCE(idempotency_key,''), attempt, created_at, expires_at, reminded_at, escalated_at, responded_at FROM approval_requests`

type approvalScanner interface{ Scan(...any) error }

func (c *Client) scanApproval(row approvalScanner) (*ApprovalRequest, error) {
	req, err := scanApprovalRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return req, err
}
func scanApprovalRow(row approvalScanner) (*ApprovalRequest, error) {
	var req ApprovalRequest
	var approvers, delegates, fields, values string
	err := row.Scan(&req.ID, &req.WorkflowInstanceID, &req.TaskID, &req.WorkflowID, &req.StepID, &req.Message, &approvers, &delegates, &req.RequiredApprovals, &fields, &req.Status, &values, &req.Feedback, &req.RespondedBy, &req.ResponseChannel, &req.IdempotencyKey, &req.Attempt, &req.CreatedAt, &req.ExpiresAt, &req.RemindedAt, &req.EscalatedAt, &req.RespondedAt)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(approvers), &req.Approvers)
	_ = json.Unmarshal([]byte(delegates), &req.Delegates)
	_ = json.Unmarshal([]byte(fields), &req.Fields)
	_ = json.Unmarshal([]byte(values), &req.Values)
	return &req, nil
}
func (c *Client) getApprovalByKey(ctx context.Context, key string) (*ApprovalRequest, error) {
	return c.scanApproval(c.db.QueryRowContext(ctx, approvalSelect+` WHERE idempotency_key=?`, key))
}
