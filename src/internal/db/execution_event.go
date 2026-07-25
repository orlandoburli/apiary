package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/orlandoburli/apiary/internal/redact"
)

const ExecutionEventSchemaVersion = 1

// ExecutionEvent is the stable envelope persisted and exported for lifecycle
// observability. Metadata is always redacted before ID assignment and delivery.
type ExecutionEvent struct {
	ID                 int64          `json:"id"`
	SchemaVersion      int            `json:"schema_version"`
	Type               string         `json:"type"`
	Timestamp          time.Time      `json:"timestamp"`
	TaskID             string         `json:"task_id,omitempty"`
	WorkflowID         string         `json:"workflow_id,omitempty"`
	WorkflowInstanceID string         `json:"workflow_instance_id,omitempty"`
	StepID             string         `json:"step_id,omitempty"`
	AttemptID          string         `json:"attempt_id,omitempty"`
	Metadata           map[string]any `json:"metadata,omitempty"`
}

// ExecutionEventFilter selects persisted events. Results are chronological.
type ExecutionEventFilter struct {
	TaskID             string
	WorkflowInstanceID string
	Type               string
	AfterID            int64
	Limit              int
}

// SetEventSensitiveFields replaces the configured metadata-key denylist. The
// built-in secret keys and token-like value detection always remain active.
func (c *Client) SetEventSensitiveFields(fields []string) {
	set := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if key := normalizeSensitiveKey(field); key != "" {
			set[key] = struct{}{}
		}
	}
	c.eventMu.Lock()
	c.eventSensitive = set
	c.eventMu.Unlock()
}

// RecordExecutionEvent persists first, then broadcasts the stored/redacted
// envelope best-effort. Slow live subscribers recover with an after_id query.
func (c *Client) RecordExecutionEvent(ctx context.Context, event *ExecutionEvent) error {
	if event == nil || strings.TrimSpace(event.Type) == "" {
		return fmt.Errorf("execution event type is required")
	}
	stored := *event
	stored.SchemaVersion = ExecutionEventSchemaVersion
	if stored.Timestamp.IsZero() {
		stored.Timestamp = time.Now().UTC()
	} else {
		stored.Timestamp = stored.Timestamp.UTC()
	}
	stored.Metadata = c.redactEventMetadata(event.Metadata)
	metadata, err := json.Marshal(stored.Metadata)
	if err != nil {
		return fmt.Errorf("marshal execution event metadata: %w", err)
	}
	res, err := c.db.ExecContext(ctx, `
		INSERT INTO execution_events
		  (schema_version, type, timestamp, task_id, workflow_id, workflow_instance_id, step_id, attempt_id, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, stored.SchemaVersion, stored.Type, stored.Timestamp, nullStr(stored.TaskID), nullStr(stored.WorkflowID),
		nullStr(stored.WorkflowInstanceID), nullStr(stored.StepID), nullStr(stored.AttemptID), string(metadata))
	if err != nil {
		return err
	}
	stored.ID, err = res.LastInsertId()
	if err != nil {
		return err
	}
	*event = stored
	c.publishExecutionEvent(stored)
	return nil
}

// ListExecutionEvents returns persisted events oldest-first.
func (c *Client) ListExecutionEvents(ctx context.Context, filter ExecutionEventFilter) ([]ExecutionEvent, error) {
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
	if filter.Type != "" {
		where = append(where, "type = ?")
		args = append(args, filter.Type)
	}
	args = append(args, limit)
	rows, err := c.db.QueryContext(ctx, `
		SELECT id, schema_version, type, timestamp, COALESCE(task_id,''), COALESCE(workflow_id,''),
		       COALESCE(workflow_instance_id,''), COALESCE(step_id,''), COALESCE(attempt_id,''), metadata
		FROM execution_events WHERE `+strings.Join(where, " AND ")+` ORDER BY id ASC LIMIT ?
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []ExecutionEvent
	for rows.Next() {
		var event ExecutionEvent
		var metadata string
		if err := rows.Scan(&event.ID, &event.SchemaVersion, &event.Type, &event.Timestamp, &event.TaskID,
			&event.WorkflowID, &event.WorkflowInstanceID, &event.StepID, &event.AttemptID, &metadata); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(metadata), &event.Metadata); err != nil {
			event.Metadata = map[string]any{"decode_error": true}
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// SubscribeExecutionEvents registers a bounded live subscriber.
func (c *Client) SubscribeExecutionEvents(buffer int) (<-chan ExecutionEvent, func()) {
	if buffer <= 0 {
		buffer = 64
	}
	c.eventMu.Lock()
	if c.eventSubs == nil {
		c.eventSubs = map[uint64]chan ExecutionEvent{}
	}
	c.eventSeq++
	id := c.eventSeq
	ch := make(chan ExecutionEvent, buffer)
	c.eventSubs[id] = ch
	c.eventMu.Unlock()
	return ch, func() {
		c.eventMu.Lock()
		if existing, ok := c.eventSubs[id]; ok {
			delete(c.eventSubs, id)
			close(existing)
		}
		c.eventMu.Unlock()
	}
}

// PruneExecutionEventsBefore deletes events older than cutoff.
func (c *Client) PruneExecutionEventsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := c.db.ExecContext(ctx, `DELETE FROM execution_events WHERE timestamp < ?`, cutoff.UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (c *Client) publishExecutionEvent(event ExecutionEvent) {
	c.eventMu.RLock()
	defer c.eventMu.RUnlock()
	for _, ch := range c.eventSubs {
		select {
		case ch <- event:
		default:
		}
	}
}

func (c *Client) redactEventMetadata(metadata map[string]any) map[string]any {
	if metadata == nil {
		return map[string]any{}
	}
	c.eventMu.RLock()
	configured := make(map[string]struct{}, len(c.eventSensitive))
	for key := range c.eventSensitive {
		configured[key] = struct{}{}
	}
	c.eventMu.RUnlock()
	redacted, _ := redactEventValue(metadata, configured).(map[string]any)
	return redacted
}

var builtInSensitiveKeys = map[string]struct{}{
	"token": {}, "accesstoken": {}, "refreshtoken": {}, "secret": {}, "clientsecret": {},
	"password": {}, "authorization": {}, "apikey": {}, "privatekey": {}, "credential": {},
}

func redactEventValue(value any, configured map[string]struct{}) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, child := range v {
			normalized := normalizeSensitiveKey(key)
			_, builtIn := builtInSensitiveKeys[normalized]
			_, custom := configured[normalized]
			if builtIn || custom {
				out[key] = "[REDACTED]"
			} else {
				out[key] = redactEventValue(child, configured)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i := range v {
			out[i] = redactEventValue(v[i], configured)
		}
		return out
	case string:
		if redact.LooksLikeSecret(v) {
			return "[REDACTED]"
		}
		return v
	default:
		return value
	}
}

func normalizeSensitiveKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	return strings.NewReplacer("_", "", "-", "", ".", "").Replace(key)
}

