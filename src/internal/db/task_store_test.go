package db

import (
	"context"
	"testing"

	"github.com/orlandoburli/apiary/internal/model"
)

func TestInternalTask_CRUD(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	store := c.InternalTasks()

	task := &model.InternalTask{
		Title:       "Investigate payments incident",
		Description: "critical alert from log monitor",
		Input:       map[string]any{"service": "payments", "severity": "critical"},
		Metadata: model.TaskMetadata{
			Labels:   []string{"apiary", "incident"},
			Priority: "high",
			Type:     "log_event",
		},
	}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatalf("create: %v", err)
	}
	if task.ID == "" {
		t.Fatal("expected generated ID, got empty")
	}
	if task.State != model.TaskStateRegistered {
		t.Errorf("default state = %q, want registered", task.State)
	}

	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected task, got nil")
	}
	if got.Title != task.Title || got.Description != task.Description {
		t.Errorf("scalar fields wrong: %+v", got)
	}
	if got.Input["service"] != "payments" || got.Input["severity"] != "critical" {
		t.Errorf("input round-trip wrong: %#v", got.Input)
	}
	if got.Metadata.Priority != "high" || got.Metadata.Type != "log_event" ||
		len(got.Metadata.Labels) != 2 {
		t.Errorf("metadata round-trip wrong: %#v", got.Metadata)
	}
}

func TestInternalTask_GetMissing(t *testing.T) {
	ctx := context.Background()
	store := newTestClient(t).InternalTasks()
	got, err := store.GetTask(ctx, "does-not-exist")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing task, got %+v", got)
	}
}

func TestInternalTask_NilInputStoredAsNull(t *testing.T) {
	ctx := context.Background()
	store := newTestClient(t).InternalTasks()
	task := &model.InternalTask{Title: "no input"}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Input != nil {
		t.Errorf("expected nil Input, got %#v", got.Input)
	}
}

func TestInternalTask_UpdateState(t *testing.T) {
	ctx := context.Background()
	store := newTestClient(t).InternalTasks()
	task := &model.InternalTask{Title: "t"}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.UpdateTaskState(ctx, task.ID, model.TaskStateRunning); err != nil {
		t.Fatalf("update state: %v", err)
	}
	got, _ := store.GetTask(ctx, task.ID)
	if got.State != model.TaskStateRunning {
		t.Errorf("state = %q, want running", got.State)
	}
}

func TestInternalTask_OutstandingCounter(t *testing.T) {
	ctx := context.Background()
	store := newTestClient(t).InternalTasks()
	task := &model.InternalTask{Title: "t"}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatalf("create: %v", err)
	}

	n, err := store.IncrementOutstanding(ctx, task.ID, 3)
	if err != nil {
		t.Fatalf("increment: %v", err)
	}
	if n != 3 {
		t.Errorf("after +3, count = %d, want 3", n)
	}

	for i, want := range []int{2, 1, 0} {
		n, err := store.DecrementOutstanding(ctx, task.ID)
		if err != nil {
			t.Fatalf("decrement %d: %v", i, err)
		}
		if n != want {
			t.Errorf("decrement %d: count = %d, want %d", i, n, want)
		}
	}

	// Clamped at zero — never negative.
	n, err = store.DecrementOutstanding(ctx, task.ID)
	if err != nil {
		t.Fatalf("decrement past zero: %v", err)
	}
	if n != 0 {
		t.Errorf("decrement past zero: count = %d, want 0", n)
	}
}

func TestInternalTask_ListByState(t *testing.T) {
	ctx := context.Background()
	store := newTestClient(t).InternalTasks()

	for i := 0; i < 3; i++ {
		if err := store.CreateTask(ctx, &model.InternalTask{
			Title: "running", State: model.TaskStateRunning,
		}); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	if err := store.CreateTask(ctx, &model.InternalTask{
		Title: "done", State: model.TaskStateDone,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	running, err := store.ListTasksByState(ctx, model.TaskStateRunning)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(running) != 3 {
		t.Errorf("running count = %d, want 3", len(running))
	}
	done, _ := store.ListTasksByState(ctx, model.TaskStateDone)
	if len(done) != 1 {
		t.Errorf("done count = %d, want 1", len(done))
	}
}

func TestSourceBinding_CRUD(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	tasks := c.InternalTasks()
	bindings := c.SourceBindings()

	task := &model.InternalTask{Title: "bound task"}
	if err := tasks.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	b := &model.SourceBinding{
		TaskID:           task.ID,
		SourceID:         "github",
		SourceItemID:     "12345",
		SourceItemURL:    "https://github.com/o/r/issues/42",
		SourceItemNumber: "#42",
	}
	if err := bindings.CreateBinding(ctx, b); err != nil {
		t.Fatalf("create binding: %v", err)
	}
	if b.ID == "" {
		t.Fatal("expected generated binding ID")
	}

	got, err := bindings.GetBindingBySourceItem(ctx, "github", "12345")
	if err != nil {
		t.Fatalf("get binding: %v", err)
	}
	if got == nil {
		t.Fatal("expected binding, got nil")
	}
	if got.TaskID != task.ID || got.SourceItemNumber != "#42" {
		t.Errorf("binding fields wrong: %+v", got)
	}

	list, err := bindings.ListBindingsByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("list bindings: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("bindings count = %d, want 1", len(list))
	}
}

func TestSourceBinding_GetMissing(t *testing.T) {
	ctx := context.Background()
	bindings := newTestClient(t).SourceBindings()
	got, err := bindings.GetBindingBySourceItem(ctx, "github", "nope")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing binding, got %+v", got)
	}
}

func TestSourceBinding_UniqueConstraint(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	task := &model.InternalTask{Title: "t"}
	if err := c.InternalTasks().CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	bindings := c.SourceBindings()
	first := &model.SourceBinding{TaskID: task.ID, SourceID: "github", SourceItemID: "1"}
	if err := bindings.CreateBinding(ctx, first); err != nil {
		t.Fatalf("first binding: %v", err)
	}
	dup := &model.SourceBinding{TaskID: task.ID, SourceID: "github", SourceItemID: "1"}
	if err := bindings.CreateBinding(ctx, dup); err == nil {
		t.Error("expected unique-constraint error on duplicate (source_id, source_item_id)")
	}
}
