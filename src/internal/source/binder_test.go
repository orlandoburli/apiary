package source

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

func newBinderTestDB(t *testing.T) *db.Client {
	t.Helper()
	path := filepath.Join(t.TempDir(), "binder.db")
	c, err := db.New(context.Background(), path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func sampleItem() model.SourceItem {
	return model.SourceItem{
		ID:          "42",
		SourceID:    "github",
		Number:      "#42",
		Title:       "Fix the widget",
		Description: "the widget is broken",
		Labels:      []string{"bug", "apiary"},
		Type:        "issue",
		Priority:    "high",
		URL:         "https://github.com/acme/repo/issues/42",
	}
}

func TestBind_NewItemCreatesTaskAndBinding(t *testing.T) {
	ctx := context.Background()
	c := newBinderTestDB(t)
	binder := NewSourceBinder(c)
	item := sampleItem()

	task, err := binder.Bind(ctx, item)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}

	if task.ID == "" {
		t.Fatal("expected generated task ID")
	}
	if task.State != model.TaskStateRegistered {
		t.Errorf("state = %q, want registered", task.State)
	}
	if task.Title != item.Title || task.Description != item.Description {
		t.Errorf("scalar fields not copied: %+v", task)
	}
	if task.Metadata.Priority != "high" || task.Metadata.Type != "issue" || len(task.Metadata.Labels) != 2 {
		t.Errorf("metadata not mapped: %#v", task.Metadata)
	}
	if task.Input != nil {
		t.Errorf("source-bound task should have nil Input, got %#v", task.Input)
	}

	// The binding must exist and point at the task.
	binding, err := c.SourceBindings().GetBindingBySourceItem(ctx, item.SourceID, item.ID)
	if err != nil {
		t.Fatalf("get binding: %v", err)
	}
	if binding == nil {
		t.Fatal("expected a binding to be created")
	}
	if binding.TaskID != task.ID {
		t.Errorf("binding.TaskID = %q, want %q", binding.TaskID, task.ID)
	}
	if binding.SourceItemNumber != item.Number || binding.SourceItemURL != item.URL {
		t.Errorf("binding ref fields wrong: %+v", binding)
	}
}

func TestBind_SecondPollReturnsExistingTask(t *testing.T) {
	ctx := context.Background()
	c := newBinderTestDB(t)
	binder := NewSourceBinder(c)
	item := sampleItem()

	first, err := binder.Bind(ctx, item)
	if err != nil {
		t.Fatalf("first bind: %v", err)
	}

	// Re-poll: same source item must resolve to the same task, with no duplicate.
	second, err := binder.Bind(ctx, item)
	if err != nil {
		t.Fatalf("second bind: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("second bind created a new task %q, want %q", second.ID, first.ID)
	}

	bindings, err := c.SourceBindings().ListBindingsByTask(ctx, first.ID)
	if err != nil {
		t.Fatalf("list bindings: %v", err)
	}
	if len(bindings) != 1 {
		t.Errorf("expected exactly 1 binding, got %d", len(bindings))
	}
}

func TestBind_ConcurrentSameItemDeduplicates(t *testing.T) {
	ctx := context.Background()
	c := newBinderTestDB(t)
	binder := NewSourceBinder(c)
	item := sampleItem()

	const n = 8
	var wg sync.WaitGroup
	ids := make([]string, n)
	errs := make([]error, n)
	start := make(chan struct{})

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // line everyone up to maximize contention
			task, err := binder.Bind(ctx, item)
			ids[i] = task.ID
			errs[i] = err
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: bind error: %v", i, err)
		}
	}

	// Every concurrent bind must resolve to the same single task ID.
	first := ids[0]
	if first == "" {
		t.Fatal("empty task ID")
	}
	for i, id := range ids {
		if id != first {
			t.Errorf("goroutine %d got task %q, want %q", i, id, first)
		}
	}

	// And there must be exactly one binding and one task overall.
	bindings, err := c.SourceBindings().ListBindingsByTask(ctx, first)
	if err != nil {
		t.Fatalf("list bindings: %v", err)
	}
	if len(bindings) != 1 {
		t.Errorf("expected exactly 1 binding after concurrent bind, got %d", len(bindings))
	}
	registered, err := c.InternalTasks().ListTasksByState(ctx, model.TaskStateRegistered)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(registered) != 1 {
		t.Errorf("expected exactly 1 task, got %d (orphan task created on race)", len(registered))
	}
}

// DefaultSourceBinder must satisfy the SourceBinder interface.
var _ SourceBinder = (*DefaultSourceBinder)(nil)
