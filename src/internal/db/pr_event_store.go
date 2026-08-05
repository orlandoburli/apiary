package db

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// GetPREventWatermark returns the PR event watermark for a source, or the zero
// time when the source has never polled events.
func (c *Client) GetPREventWatermark(ctx context.Context, sourceID string) (time.Time, error) {
	var wm time.Time
	err := c.db.QueryRowContext(ctx,
		`SELECT watermark FROM pr_event_watermarks WHERE source_id = ?`, sourceID).Scan(&wm)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return wm, nil
}

// SetPREventWatermark upserts the PR event watermark for a source.
func (c *Client) SetPREventWatermark(ctx context.Context, sourceID string, watermark time.Time) error {
	_, err := c.db.ExecContext(ctx, `
		INSERT INTO pr_event_watermarks (source_id, watermark) VALUES (?, ?)
		ON CONFLICT(source_id) DO UPDATE SET watermark = excluded.watermark
	`, sourceID, watermark)
	return err
}

// ClaimPREventDispatch atomically claims the (source, event, workflow) dispatch:
// it returns true when this call inserted the claim (the caller must dispatch)
// and false when the claim already existed (already dispatched — skip). The
// insert-or-ignore against the primary key is what makes an event dispatch a
// given workflow exactly once, across poll overlaps and daemon restarts.
func (c *Client) ClaimPREventDispatch(ctx context.Context, sourceID, eventID, workflowID string, prNumber int) (bool, error) {
	res, err := c.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO pr_event_dispatches (source_id, event_id, workflow_id, pr_number)
		VALUES (?, ?, ?, ?)
	`, sourceID, eventID, workflowID, prNumber)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// CountPREventDispatches returns how many events have dispatched the workflow
// for one pull request — the denominator of the trigger's max_dispatches
// runaway-loop budget.
func (c *Client) CountPREventDispatches(ctx context.Context, sourceID, workflowID string, prNumber int) (int, error) {
	var n int
	err := c.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM pr_event_dispatches
		WHERE source_id = ? AND workflow_id = ? AND pr_number = ?
	`, sourceID, workflowID, prNumber).Scan(&n)
	return n, err
}
