package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/orlandoburli/apiary/internal/audit"
)

// AuditEvent is one persisted tool call from an agent subprocess (SEC-14).
type AuditEvent struct {
	ID                 int64
	TaskID             string
	WorkflowInstanceID string
	StepID             string
	ExecutionID        int64
	ToolName           string
	InputSummary       string
	AnomalyFlags       []audit.Flag
	IsAnomalous        bool
	OccurredAt         time.Time
}

// AuditEventFilter selects persisted audit events for ListAuditEvents.
type AuditEventFilter struct {
	TaskID             string
	WorkflowInstanceID string
	AnomalousOnly      bool
	AfterID            int64
	Limit              int
}

// RecordAuditEvent persists one agent tool-call event. Errors are non-fatal
// callers log and continue — audit failures must never block agent execution.
func (c *Client) RecordAuditEvent(ctx context.Context, ev *AuditEvent) error {
	if ev == nil || strings.TrimSpace(ev.ToolName) == "" {
		return fmt.Errorf("audit event tool_name is required")
	}
	flags, err := json.Marshal(ev.AnomalyFlags)
	if err != nil {
		flags = []byte("[]")
	}
	isAnomalous := 0
	if ev.IsAnomalous {
		isAnomalous = 1
	}
	var execID any
	if ev.ExecutionID > 0 {
		execID = ev.ExecutionID
	}
	res, err := c.db.ExecContext(ctx, `
		INSERT INTO agent_audit_events
		  (task_id, workflow_instance_id, step_id, execution_id, tool_name, input_summary,
		   anomaly_flags, is_anomalous, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, ev.TaskID, nullStr(ev.WorkflowInstanceID), nullStr(ev.StepID), execID,
		ev.ToolName, nullStr(ev.InputSummary), string(flags), isAnomalous, ev.OccurredAt.UTC())
	if err != nil {
		return err
	}
	ev.ID, _ = res.LastInsertId()
	return nil
}

// ListAuditEvents returns persisted audit events oldest-first.
func (c *Client) ListAuditEvents(ctx context.Context, filter AuditEventFilter) ([]AuditEvent, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	where := []string{"id > ?"}
	args := []any{filter.AfterID}
	if filter.TaskID != "" {
		where = append(where, "task_id = ?")
		args = append(args, filter.TaskID)
	}
	if filter.WorkflowInstanceID != "" {
		where = append(where, "workflow_instance_id = ?")
		args = append(args, filter.WorkflowInstanceID)
	}
	if filter.AnomalousOnly {
		where = append(where, "is_anomalous = 1")
	}
	args = append(args, limit)

	rows, err := c.db.QueryContext(ctx, `
		SELECT id, task_id, COALESCE(workflow_instance_id,''), COALESCE(step_id,''),
		       COALESCE(execution_id,0), tool_name, COALESCE(input_summary,''),
		       anomaly_flags, is_anomalous, occurred_at
		FROM agent_audit_events
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY id ASC LIMIT ?
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []AuditEvent
	for rows.Next() {
		var ev AuditEvent
		var flagsJSON string
		var isAnomalous int
		if err := rows.Scan(&ev.ID, &ev.TaskID, &ev.WorkflowInstanceID, &ev.StepID,
			&ev.ExecutionID, &ev.ToolName, &ev.InputSummary,
			&flagsJSON, &isAnomalous, &ev.OccurredAt); err != nil {
			return nil, err
		}
		ev.IsAnomalous = isAnomalous != 0
		_ = json.Unmarshal([]byte(flagsJSON), &ev.AnomalyFlags)
		events = append(events, ev)
	}
	return events, rows.Err()
}
