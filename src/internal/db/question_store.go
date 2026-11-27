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

// Question statuses. A question is only ever pending once; answering is a
// one-way transition, which is what makes the long-poll idempotent.
const (
	QuestionPending   = "pending"
	QuestionAnswered  = "answered"
	QuestionTimedOut  = "timed_out"
	QuestionCancelled = "cancelled"
)

// QuestionOption is one answer an ask_human question offers. It mirrors the
// MCP tool schema rather than importing it so the store stays transport-neutral.
type QuestionOption struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
}

// AgentQuestion is one mid-run question from an agent to a human.
//
// The per-run ids (workflow instance, step, task, agent) are captured at ask
// time: the MCP server is launched once per provider, but the question must
// point at the run that asked it, otherwise a dashboard cannot show who is
// blocked on what.
type AgentQuestion struct {
	ID                 string           `json:"id"`
	WorkflowInstanceID string           `json:"workflow_instance_id,omitempty"`
	StepID             string           `json:"step_id,omitempty"`
	TaskID             string           `json:"task_id,omitempty"`
	AgentID            string           `json:"agent_id,omitempty"`
	Question           string           `json:"question"`
	Options            []QuestionOption `json:"options"`
	AllowFreeText      bool             `json:"allow_free_text"`
	Status             string           `json:"status"`
	Answer             string           `json:"answer,omitempty"`
	AnswerReason       string           `json:"answer_reason,omitempty"`
	AnsweredBy         string           `json:"answered_by,omitempty"`
	CreatedAt          time.Time        `json:"created_at"`
	ExpiresAt          *time.Time       `json:"expires_at,omitempty"`
	AnsweredAt         *time.Time       `json:"answered_at,omitempty"`
}

// CreateQuestion persists a pending question. The id is generated here so the
// agent never has to choose one (and cannot collide with another run's id).
func (c *Client) CreateQuestion(ctx context.Context, q *AgentQuestion) error {
	if strings.TrimSpace(q.Question) == "" {
		return fmt.Errorf("question is required")
	}
	if q.ID == "" {
		q.ID = newID()
	}
	if q.Status == "" {
		q.Status = QuestionPending
	}
	if q.CreatedAt.IsZero() {
		q.CreatedAt = time.Now().UTC()
	}
	if q.Options == nil {
		q.Options = []QuestionOption{}
	}
	options, err := json.Marshal(q.Options)
	if err != nil {
		return err
	}
	_, err = c.db.ExecContext(ctx,
		`INSERT INTO agent_questions (id, workflow_instance_id, step_id, task_id, agent_id, question, options, allow_free_text, status, created_at, expires_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		q.ID, nullStr(q.WorkflowInstanceID), nullStr(q.StepID), nullStr(q.TaskID), nullStr(q.AgentID),
		q.Question, string(options), q.AllowFreeText, q.Status, q.CreatedAt, q.ExpiresAt)
	return err
}

// GetQuestion returns the row, or (nil, nil) when the id is unknown. Callers
// distinguish "not found" from a storage failure so HTTP can answer 404 versus
// 500 without a sentinel error.
func (c *Client) GetQuestion(ctx context.Context, id string) (*AgentQuestion, error) {
	q, err := scanQuestion(c.db.QueryRowContext(ctx, questionSelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return q, err
}

// AnswerQuestion records an answer only while the question is still pending and
// unexpired, and reports whether this call won that transition. A long-poll and
// a dashboard can race for the same question; exactly one answer sticks, and
// the loser re-reads the stored row instead of overwriting it.
func (c *Client) AnswerQuestion(ctx context.Context, id, answer, reason, actor string) (*AgentQuestion, bool, error) {
	now := time.Now().UTC()
	res, err := c.db.ExecContext(ctx,
		`UPDATE agent_questions SET status=?, answer=?, answer_reason=?, answered_by=?, answered_at=?
		 WHERE id=? AND status=? AND (expires_at IS NULL OR expires_at > ?)`,
		QuestionAnswered, answer, nullStr(reason), nullStr(actor), now, id, QuestionPending, now)
	if err != nil {
		return nil, false, err
	}
	n, _ := res.RowsAffected()
	q, getErr := c.GetQuestion(ctx, id)
	return q, n == 1, getErr
}

// ExpireQuestions closes every pending question whose deadline has passed. It
// is a sweep rather than a timer: the only observers are long-polls (which
// re-check every ~250ms) and the dashboard, so a periodic UPDATE on read is
// simpler than a scheduler and cannot leak a goroutine per question.
func (c *Client) ExpireQuestions(ctx context.Context) (int64, error) {
	res, err := c.db.ExecContext(ctx,
		`UPDATE agent_questions SET status=? WHERE status=? AND expires_at IS NOT NULL AND expires_at <= ?`,
		QuestionTimedOut, QuestionPending, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (c *Client) ListPendingQuestions(ctx context.Context, limit int) ([]AgentQuestion, error) {
	return c.ListQuestions(ctx, QuestionPending, limit)
}

// HasPendingQuestion reports whether the given step of a workflow instance is
// currently blocked on an unanswered question. The runner asks this before
// treating silence as a hang. An expired-but-not-yet-swept row does not count:
// the asking process is already gone or past its deadline.
func (c *Client) HasPendingQuestion(ctx context.Context, workflowInstanceID, stepID string) (bool, error) {
	if workflowInstanceID == "" || stepID == "" {
		return false, nil
	}
	var n int
	err := c.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agent_questions
		 WHERE workflow_instance_id=? AND step_id=? AND status=? AND (expires_at IS NULL OR expires_at > ?)`,
		workflowInstanceID, stepID, QuestionPending, time.Now().UTC()).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ExpireStepQuestions closes every pending question left behind by one step run.
// When the process exits — answered, stalled, timed out, crashed or cancelled —
// nobody is left to receive an answer, so the dashboard must stop offering the
// question. Abandoned questions are recorded as timed_out: the run's deadline or
// death is the timeout, and no new status is needed for it.
func (c *Client) ExpireStepQuestions(ctx context.Context, workflowInstanceID, stepID string) (int64, error) {
	if workflowInstanceID == "" || stepID == "" {
		// An empty id pair would widen the UPDATE to unrelated rows, including
		// plain-route questions that carry no workflow identity.
		return 0, nil
	}
	res, err := c.db.ExecContext(ctx,
		`UPDATE agent_questions SET status=? WHERE workflow_instance_id=? AND step_id=? AND status=?`,
		QuestionTimedOut, workflowInstanceID, stepID, QuestionPending)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ListQuestions returns questions newest-first, optionally filtered by status.
// The dashboard polls this to show what agents are blocked on.
func (c *Client) ListQuestions(ctx context.Context, status string, limit int) ([]AgentQuestion, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := questionSelect
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
	var out []AgentQuestion
	for rows.Next() {
		q, err := scanQuestion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *q)
	}
	return out, rows.Err()
}

const questionSelect = `SELECT id, COALESCE(workflow_instance_id,''), COALESCE(step_id,''), COALESCE(task_id,''), COALESCE(agent_id,''), question, options, allow_free_text, status, COALESCE(answer,''), COALESCE(answer_reason,''), COALESCE(answered_by,''), created_at, expires_at, answered_at FROM agent_questions`

type questionScanner interface{ Scan(...any) error }

func scanQuestion(row questionScanner) (*AgentQuestion, error) {
	var q AgentQuestion
	var options string
	if err := row.Scan(&q.ID, &q.WorkflowInstanceID, &q.StepID, &q.TaskID, &q.AgentID, &q.Question, &options, &q.AllowFreeText, &q.Status, &q.Answer, &q.AnswerReason, &q.AnsweredBy, &q.CreatedAt, &q.ExpiresAt, &q.AnsweredAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(options), &q.Options)
	if q.Options == nil {
		q.Options = []QuestionOption{}
	}
	return &q, nil
}
