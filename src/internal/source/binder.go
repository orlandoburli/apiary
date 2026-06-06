package source

import (
	"context"
	"errors"
	"fmt"

	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

// SourceBinder turns a SourceItem (the binding-layer view of a polled source
// item) into an InternalTask — the canonical, source-independent unit of work.
// It is idempotent per (source_id, source_item_id): the same source item always
// resolves to the same InternalTask, so re-polling never creates duplicates.
type SourceBinder interface {
	// Bind returns the InternalTask for the given SourceItem, creating it (and
	// its SourceBinding) on first sight and returning the existing one on every
	// subsequent poll.
	Bind(ctx context.Context, item model.SourceItem) (model.InternalTask, error)
}

// DefaultSourceBinder is the database-backed SourceBinder. It records the
// source→task link in source_bindings and the task itself in internal_tasks.
type DefaultSourceBinder struct {
	client   *db.Client
	tasks    *db.InternalTaskStore
	bindings *db.SourceBindingStore
}

// NewSourceBinder builds a DefaultSourceBinder backed by the given client.
func NewSourceBinder(client *db.Client) *DefaultSourceBinder {
	return &DefaultSourceBinder{
		client:   client,
		tasks:    client.InternalTasks(),
		bindings: client.SourceBindings(),
	}
}

// Bind looks up an existing binding for the item; if found it returns the bound
// task. Otherwise it creates the task and binding atomically. A concurrent bind
// of the same item is resolved by re-fetching the winning task.
func (b *DefaultSourceBinder) Bind(ctx context.Context, item model.SourceItem) (model.InternalTask, error) {
	if existing, err := b.resolveExisting(ctx, item); err != nil {
		return model.InternalTask{}, err
	} else if existing != nil {
		return b.refresh(ctx, *existing, item)
	}

	task := taskFromItem(item)
	binding := bindingFromItem(item)
	err := b.client.CreateTaskWithBinding(ctx, &task, &binding)
	switch {
	case errors.Is(err, db.ErrBindingExists):
		// Lost a race with a concurrent bind of the same item — the other
		// caller's task is the winner; return it.
		existing, err := b.resolveExisting(ctx, item)
		if err != nil {
			return model.InternalTask{}, err
		}
		if existing == nil {
			return model.InternalTask{}, fmt.Errorf("bind %s/%s: binding vanished after conflict", item.SourceID, item.ID)
		}
		return *existing, nil
	case err != nil:
		return model.InternalTask{}, fmt.Errorf("bind %s/%s: %w", item.SourceID, item.ID, err)
	}
	return task, nil
}

// resolveExisting returns the task already bound to the item, or nil if none.
func (b *DefaultSourceBinder) resolveExisting(ctx context.Context, item model.SourceItem) (*model.InternalTask, error) {
	binding, err := b.bindings.GetBindingBySourceItem(ctx, item.SourceID, item.ID)
	if err != nil {
		return nil, fmt.Errorf("lookup binding %s/%s: %w", item.SourceID, item.ID, err)
	}
	if binding == nil {
		return nil, nil
	}
	task, err := b.tasks.GetTask(ctx, binding.TaskID)
	if err != nil {
		return nil, fmt.Errorf("fetch task %s: %w", binding.TaskID, err)
	}
	if task == nil {
		return nil, fmt.Errorf("binding %s references missing task %s", binding.ID, binding.TaskID)
	}
	return task, nil
}

// refresh keeps a source-bound task's routing attributes in step with the live
// source item on every poll. The apiary handoff flow advances a task by mutating
// its source item's labels (and state), so Router.RouteAll must observe the
// current values, not those frozen at first bind. It only rewrites title,
// description, and metadata when they actually changed; task lifecycle state,
// input, and the outstanding-workflow counter are left untouched.
func (b *DefaultSourceBinder) refresh(ctx context.Context, task model.InternalTask, item model.SourceItem) (model.InternalTask, error) {
	fresh := metaFromItem(item)
	if task.Title == item.Title && task.Description == item.Description && metaEqual(task.Metadata, fresh) {
		return task, nil
	}
	if err := b.tasks.UpdateTaskMetadata(ctx, task.ID, item.Title, item.Description, fresh); err != nil {
		return model.InternalTask{}, fmt.Errorf("refresh task %s from %s/%s: %w", task.ID, item.SourceID, item.ID, err)
	}
	task.Title = item.Title
	task.Description = item.Description
	task.Metadata = fresh
	return task, nil
}

// metaFromItem maps a source item's routing-relevant attributes into TaskMetadata.
func metaFromItem(item model.SourceItem) model.TaskMetadata {
	return model.TaskMetadata{
		Labels:   item.Labels,
		Priority: item.Priority,
		Type:     item.Type,
		Source:   item.SourceID,
		State:    item.State,
	}
}

// metaEqual reports whether two TaskMetadata carry the same routing attributes.
func metaEqual(a, b model.TaskMetadata) bool {
	if a.Priority != b.Priority || a.Type != b.Type || a.Source != b.Source || a.State != b.State {
		return false
	}
	if len(a.Labels) != len(b.Labels) {
		return false
	}
	for i := range a.Labels {
		if a.Labels[i] != b.Labels[i] {
			return false
		}
	}
	return true
}

// taskFromItem builds a registered InternalTask from a source item. Input is left
// nil: structured input is only set for spawned tasks, never source-bound ones.
func taskFromItem(item model.SourceItem) model.InternalTask {
	return model.InternalTask{
		Title:       item.Title,
		Description: item.Description,
		State:       model.TaskStateRegistered,
		Metadata:    metaFromItem(item),
	}
}

// bindingFromItem builds a SourceBinding for an item. TaskID is filled in by
// CreateTaskWithBinding once the task ID is known.
func bindingFromItem(item model.SourceItem) model.SourceBinding {
	return model.SourceBinding{
		SourceID:         item.SourceID,
		SourceItemID:     item.ID,
		SourceItemURL:    item.URL,
		SourceItemNumber: item.Number,
	}
}
