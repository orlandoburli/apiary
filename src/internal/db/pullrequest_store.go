package db

import "context"

// TaskPullRequest is one pull request linked to an InternalTask, discovered from
// the source (e.g. a GitHub issue's cross-referenced PRs). Seq is the source-order
// position; the row with the highest Seq is the most recent PR.
type TaskPullRequest struct {
	ID       string
	TaskID   string
	SourceID string
	PRNumber int
	PRURL    string
	PRState  string
	Seq      int
}

// ReplaceTaskPullRequests atomically swaps the persisted PR set for a single
// (taskID, sourceID) pair: it deletes the existing rows for that pair and inserts
// prs in order (seq = slice index). Callers MUST invoke this only after a
// successful source listing — a transient/auth error should skip the call so the
// last-good data survives. Other sources' rows for the same task are untouched.
func (c *Client) ReplaceTaskPullRequests(ctx context.Context, taskID, sourceID string, prs []TaskPullRequest) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM task_pull_requests WHERE task_id = ? AND source_id = ?`,
		taskID, sourceID); err != nil {
		return err
	}
	for i, p := range prs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO task_pull_requests
			  (id, task_id, source_id, pr_number, pr_url, pr_state, seq)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, newID(), taskID, sourceID, p.PRNumber, p.PRURL, nullStr(p.PRState), i); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListTaskPullRequests returns all PRs linked to a task across sources, ordered
// oldest first (seq ASC). The tail is the most recent PR — what the dashboard's
// "open PR" shortcut opens.
func (c *Client) ListTaskPullRequests(ctx context.Context, taskID string) ([]TaskPullRequest, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT id, task_id, source_id, pr_number, pr_url, COALESCE(pr_state,''), seq
		FROM task_pull_requests WHERE task_id = ?
		ORDER BY seq ASC, pr_number ASC
	`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TaskPullRequest
	for rows.Next() {
		var p TaskPullRequest
		if err := rows.Scan(&p.ID, &p.TaskID, &p.SourceID, &p.PRNumber,
			&p.PRURL, &p.PRState, &p.Seq); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
