package db

import (
	"context"
	"fmt"
	"time"
)

// ScrubPromptsBefore NULLs input_prompt/output_text on task_executions and
// input_prompt on step_runs whose started_at is older than cutoff, while
// leaving all token-count and cost columns intact. Only rows that still carry
// non-NULL prompt text are counted; already-scrubbed rows are a no-op.
// Returns the number of rows actually scrubbed.
func (c *Client) ScrubPromptsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	var total int64

	// task_executions: NULL both input_prompt and output_text.
	res, err := c.db.ExecContext(ctx, `
		UPDATE task_executions
		SET input_prompt = NULL, output_text = NULL
		WHERE started_at < ?
		  AND (input_prompt IS NOT NULL OR output_text IS NOT NULL)
	`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("scrub task_executions prompts: %w", err)
	}
	n, _ := res.RowsAffected()
	total += n

	// step_runs: NULL input_prompt only (no output_text column).
	res, err = c.db.ExecContext(ctx, `
		UPDATE step_runs
		SET input_prompt = NULL
		WHERE started_at < ?
		  AND input_prompt IS NOT NULL
	`, cutoff)
	if err != nil {
		return total, fmt.Errorf("scrub step_runs prompts: %w", err)
	}
	n, _ = res.RowsAffected()
	total += n

	return total, nil
}
