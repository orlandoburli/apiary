package db

import (
	"context"
	"database/sql"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
)

// SourceBindingStore persists SourceBinding rows, which link a source item
// (source_id + source_item_id) to an InternalTask. The (source_id,
// source_item_id) pair is unique, so the same source item always resolves to
// the same task.
type SourceBindingStore struct {
	db *sql.DB
}

// SourceBindings returns a store bound to this client's database.
func (c *Client) SourceBindings() *SourceBindingStore {
	return &SourceBindingStore{db: c.db}
}

// NewSourceBindingStore wraps a raw *sql.DB. Used in tests.
func NewSourceBindingStore(db *sql.DB) *SourceBindingStore {
	return &SourceBindingStore{db: db}
}

// CreateBinding inserts a new source binding. If binding.ID is empty a new ID is
// generated and written back. A duplicate (source_id, source_item_id) violates
// the unique constraint and surfaces as an error for the caller to handle.
func (s *SourceBindingStore) CreateBinding(ctx context.Context, binding *model.SourceBinding) error {
	return insertBinding(ctx, s.db, binding)
}

// insertBinding writes a binding row using the given executor (a *sql.DB or a
// *sql.Tx), so it can participate in CreateTaskWithBinding's transaction.
func insertBinding(ctx context.Context, ex execer, binding *model.SourceBinding) error {
	if binding.ID == "" {
		binding.ID = newID()
	}
	if binding.CreatedAt.IsZero() {
		binding.CreatedAt = time.Now()
	}
	_, err := ex.ExecContext(ctx, `
		INSERT INTO source_bindings
		  (id, task_id, source_id, source_item_id, source_item_url, source_item_number, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, binding.ID, binding.TaskID, binding.SourceID, binding.SourceItemID,
		nullStr(binding.SourceItemURL), nullStr(binding.SourceItemNumber), binding.CreatedAt)
	return err
}

// GetBindingBySourceItem returns the binding for a (source_id, source_item_id)
// pair, or (nil, nil) if none exists.
func (s *SourceBindingStore) GetBindingBySourceItem(ctx context.Context, sourceID, sourceItemID string) (*model.SourceBinding, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, task_id, source_id, source_item_id, COALESCE(source_item_url,''),
		       COALESCE(source_item_number,''), created_at
		FROM source_bindings WHERE source_id = ? AND source_item_id = ?
	`, sourceID, sourceItemID)
	b, err := scanBinding(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return b, err
}

// GetBindingBySourceItemID returns the first binding whose source_item_id
// matches, regardless of source, or (nil, nil) if none exists. It resolves a bare
// source-item reference (e.g. a GitHub issue number / the dashboard DrillKey) to a
// task when the caller does not know the source id. The (source_id, source_item_id)
// pair is unique, but the same item id may exist under different sources; the oldest
// binding wins, and callers wanting precision should pass source:item instead.
func (s *SourceBindingStore) GetBindingBySourceItemID(ctx context.Context, sourceItemID string) (*model.SourceBinding, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, task_id, source_id, source_item_id, COALESCE(source_item_url,''),
		       COALESCE(source_item_number,''), created_at
		FROM source_bindings WHERE source_item_id = ? ORDER BY created_at ASC LIMIT 1
	`, sourceItemID)
	b, err := scanBinding(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return b, err
}

// DeleteBindingsByTask removes all source bindings for a task. Used when a task is
// deleted so the same source item rebinds to a fresh task on the next poll instead
// of resolving to the now-deleted task id (which the SourceBinder treats as fatal).
func (s *SourceBindingStore) DeleteBindingsByTask(ctx context.Context, taskID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM source_bindings WHERE task_id = ?`, taskID)
	return err
}

// ListBindingsByTask returns all source bindings for a task, oldest first. It is
// a Client-level convenience so the workflow engine can resolve a task's bindings
// (for side-effect fan-out) through the same Store it already holds.
func (c *Client) ListBindingsByTask(ctx context.Context, taskID string) ([]model.SourceBinding, error) {
	return c.SourceBindings().ListBindingsByTask(ctx, taskID)
}

// ListBindingsByTask returns all source bindings for a task, oldest first.
func (s *SourceBindingStore) ListBindingsByTask(ctx context.Context, taskID string) ([]model.SourceBinding, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, task_id, source_id, source_item_id, COALESCE(source_item_url,''),
		       COALESCE(source_item_number,''), created_at
		FROM source_bindings WHERE task_id = ? ORDER BY created_at ASC
	`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.SourceBinding
	for rows.Next() {
		b, err := scanBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

func scanBinding(s scanner) (*model.SourceBinding, error) {
	var b model.SourceBinding
	if err := s.Scan(&b.ID, &b.TaskID, &b.SourceID, &b.SourceItemID,
		&b.SourceItemURL, &b.SourceItemNumber, &b.CreatedAt); err != nil {
		return nil, err
	}
	return &b, nil
}
