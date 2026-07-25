package db

import (
	"context"
	"fmt"
	"time"
)

// ScrubPromptsBefore NULLs the input_prompt and output_text columns on
// task_executions rows, and input_prompt on step_runs rows, whose started_at
// is older than cutoff. Only rows that already carry a non-NULL value are
// counted, so a second call on the same cutoff is a cheap no-op.
// Returns the total number of rows scrubbed.
func (c *Client) ScrubPromptsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	const batch = 5000
	var total int64

	// task_executions: scrub input_prompt and output_text together.
	for {
		res, err := c.db.ExecContext(ctx, `
			UPDATE task_executions
			SET input_prompt = NULL, output_text = NULL
			WHERE id IN (
				SELECT id FROM task_executions
				WHERE started_at < ?
				  AND (input_prompt IS NOT NULL OR output_text IS NOT NULL)
				LIMIT ?
			)`, cutoff, batch)
		if err != nil {
			return total, fmt.Errorf("scrub task_executions: %w", err)
		}
		n, _ := res.RowsAffected()
		total += n
		if n < batch {
			break
		}
	}

	// step_runs: scrub input_prompt only.
	for {
		res, err := c.db.ExecContext(ctx, `
			UPDATE step_runs
			SET input_prompt = NULL
			WHERE id IN (
				SELECT id FROM step_runs
				WHERE started_at < ?
				  AND input_prompt IS NOT NULL
				LIMIT ?
			)`, cutoff, batch)
		if err != nil {
			return total, fmt.Errorf("scrub step_runs: %w", err)
		}
		n, _ := res.RowsAffected()
		total += n
		if n < batch {
			break
		}
	}

	return total, nil
}
