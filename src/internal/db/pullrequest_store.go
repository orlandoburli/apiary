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

// UpsertTaskPullRequest links one pull request to a task, keyed by
// (task_id, source_id, pr_number): a PR already linked has its URL and state
// refreshed instead of being duplicated, so a step that re-runs (a loop-back
// after a red CI) never stacks up rows for the same PR.
//
// Unlike ReplaceTaskPullRequests this is additive — it is fed by workflow steps
// reporting the PR they opened (pull_request_from), one at a time, rather than
// by a source listing that knows the complete set. New rows go to the end of
// the seq order, so the newest PR stays the one the dashboard opens.
func (c *Client) UpsertTaskPullRequest(ctx context.Context, taskID string, pr TaskPullRequest) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
		UPDATE task_pull_requests SET pr_url = ?, pr_state = COALESCE(?, pr_state)
		WHERE task_id = ? AND source_id = ? AND pr_number = ?
	`, pr.PRURL, nullStr(pr.PRState), taskID, pr.SourceID, pr.PRNumber)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		return tx.Commit()
	}

	var nextSeq int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq)+1, 0) FROM task_pull_requests WHERE task_id = ?`,
		taskID).Scan(&nextSeq); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO task_pull_requests
		  (id, task_id, source_id, pr_number, pr_url, pr_state, seq)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, newID(), taskID, pr.SourceID, pr.PRNumber, pr.PRURL, nullStr(pr.PRState), nextSeq); err != nil {
		return err
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
