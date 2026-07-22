package db

import (
	"context"
	"database/sql"
)

// PutWorkflowSnapshot stores the exact serialized workflow definition used by
// an instance. Replacing makes the operation idempotent for startup/retry paths.
func (c *Client) PutWorkflowSnapshot(ctx context.Context, instanceID, workflowJSON string) error {
	_, err := c.db.ExecContext(ctx, `
		INSERT INTO workflow_instance_snapshots (instance_id, workflow_json)
		VALUES (?, ?)
		ON CONFLICT(instance_id) DO UPDATE SET workflow_json = excluded.workflow_json
	`, instanceID, workflowJSON)
	return err
}

// GetWorkflowSnapshot returns the serialized workflow definition for an
// instance, or an empty string for instances created before snapshots existed.
func (c *Client) GetWorkflowSnapshot(ctx context.Context, instanceID string) (string, error) {
	var snapshot string
	err := c.db.QueryRowContext(ctx, `
		SELECT workflow_json FROM workflow_instance_snapshots WHERE instance_id = ?
	`, instanceID).Scan(&snapshot)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return snapshot, err
}
