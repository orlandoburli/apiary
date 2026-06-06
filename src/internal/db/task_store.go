package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
)

// InternalTaskStore persists InternalTask rows — the canonical, source-independent
// unit of work. It is a distinct type (not a Client method set) so its
// UpdateTaskState does not collide with the legacy Client.UpdateTaskState that
// operates on the old `tasks` table.
type InternalTaskStore struct {
	db *sql.DB
}

// InternalTasks returns a store bound to this client's database.
func (c *Client) InternalTasks() *InternalTaskStore {
	return &InternalTaskStore{db: c.db}
}

// NewInternalTaskStore wraps a raw *sql.DB. Used in tests.
func NewInternalTaskStore(db *sql.DB) *InternalTaskStore {
	return &InternalTaskStore{db: db}
}

// newID returns a lexicographically sortable, dependency-free identifier:
// a 48-bit millisecond timestamp prefix (sortable) plus 80 bits of randomness.
// This stands in for a ulid without pulling in an external library.
func newID() string {
	ts := uint64(time.Now().UnixMilli())
	var r [10]byte
	_, _ = rand.Read(r[:])
	return fmt.Sprintf("%012x%s", ts&0xFFFFFFFFFFFF, hex.EncodeToString(r[:]))
}

// CreateTask inserts a new InternalTask. If task.ID is empty a new ID is
// generated and written back to the struct. Metadata and Input are stored as
// JSON; a nil Input is stored as SQL NULL.
func (s *InternalTaskStore) CreateTask(ctx context.Context, task *model.InternalTask) error {
	if task.ID == "" {
		task.ID = newID()
	}
	now := time.Now()
	if task.CreatedAt.IsZero() {
		task.CreatedAt = now
	}
	task.UpdatedAt = now
	if task.State == "" {
		task.State = model.TaskStateRegistered
	}

	metaJSON, err := json.Marshal(task.Metadata)
	if err != nil {
		return fmt.Errorf("marshal task metadata: %w", err)
	}
	var inputJSON any
	if task.Input != nil {
		b, err := json.Marshal(task.Input)
		if err != nil {
			return fmt.Errorf("marshal task input: %w", err)
		}
		inputJSON = string(b)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO internal_tasks
		  (id, parent_task_id, title, description, input, state, metadata,
		   outstanding_workflows, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, task.ID, nullStr(task.ParentTaskID), task.Title, nullStr(task.Description),
		inputJSON, string(task.State), string(metaJSON), task.OutstandingWorkflows,
		task.CreatedAt, task.UpdatedAt)
	return err
}

// GetTask fetches a task by ID, or (nil, nil) if not found.
func (s *InternalTaskStore) GetTask(ctx context.Context, id string) (*model.InternalTask, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, COALESCE(parent_task_id,''), title, COALESCE(description,''),
		       COALESCE(input,''), state, COALESCE(metadata,''),
		       COALESCE(outstanding_workflows,0), created_at, updated_at
		FROM internal_tasks WHERE id = ?
	`, id)
	task, err := scanTask(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return task, err
}

// UpdateTaskState transitions a task to a new state.
func (s *InternalTaskStore) UpdateTaskState(ctx context.Context, id string, state model.TaskState) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE internal_tasks SET state = ?, updated_at = ? WHERE id = ?
	`, string(state), time.Now(), id)
	return err
}

// IncrementOutstanding adds delta to a task's outstanding workflow counter and
// returns the new count. The dispatcher uses this at fan-out time (delta = N).
func (s *InternalTaskStore) IncrementOutstanding(ctx context.Context, id string, delta int) (int, error) {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE internal_tasks
		SET outstanding_workflows = outstanding_workflows + ?, updated_at = ?
		WHERE id = ?
	`, delta, time.Now(), id); err != nil {
		return 0, err
	}
	return s.outstanding(ctx, id)
}

// DecrementOutstanding subtracts one from a task's outstanding workflow counter
// (clamped at zero) and returns the new count. Called when a workflow instance
// reaches a terminal state.
func (s *InternalTaskStore) DecrementOutstanding(ctx context.Context, id string) (int, error) {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE internal_tasks
		SET outstanding_workflows = MAX(outstanding_workflows - 1, 0), updated_at = ?
		WHERE id = ?
	`, time.Now(), id); err != nil {
		return 0, err
	}
	return s.outstanding(ctx, id)
}

func (s *InternalTaskStore) outstanding(ctx context.Context, id string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(outstanding_workflows,0) FROM internal_tasks WHERE id = ?`, id).Scan(&n)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return n, err
}

// ListTasksByState returns all tasks in the given state, oldest first.
func (s *InternalTaskStore) ListTasksByState(ctx context.Context, state model.TaskState) ([]model.InternalTask, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, COALESCE(parent_task_id,''), title, COALESCE(description,''),
		       COALESCE(input,''), state, COALESCE(metadata,''),
		       COALESCE(outstanding_workflows,0), created_at, updated_at
		FROM internal_tasks WHERE state = ? ORDER BY created_at ASC
	`, string(state))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.InternalTask
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *task)
	}
	return out, rows.Err()
}

func scanTask(s scanner) (*model.InternalTask, error) {
	var (
		task      model.InternalTask
		inputJSON string
		metaJSON  string
		state     string
	)
	if err := s.Scan(&task.ID, &task.ParentTaskID, &task.Title, &task.Description,
		&inputJSON, &state, &metaJSON, &task.OutstandingWorkflows,
		&task.CreatedAt, &task.UpdatedAt); err != nil {
		return nil, err
	}
	task.State = model.TaskState(state)
	if metaJSON != "" {
		if err := json.Unmarshal([]byte(metaJSON), &task.Metadata); err != nil {
			return nil, fmt.Errorf("unmarshal task metadata: %w", err)
		}
	}
	if inputJSON != "" {
		if err := json.Unmarshal([]byte(inputJSON), &task.Input); err != nil {
			return nil, fmt.Errorf("unmarshal task input: %w", err)
		}
	}
	return &task, nil
}
