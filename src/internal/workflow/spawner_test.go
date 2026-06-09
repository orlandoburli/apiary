package workflow

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
)

// fakeCreator records created tasks. CreateTask mimics the store's behaviour of
// leaving a caller-supplied id in place, and FindChildByDedupKey mimics the
// (parent_task_id, dedup_key) unique index so idempotency can be exercised.
type fakeCreator struct {
	mu    sync.Mutex
	tasks []model.InternalTask
}

func (f *fakeCreator) CreateTask(_ context.Context, t *model.InternalTask) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t.DedupKey != "" {
		for _, existing := range f.tasks {
			if existing.ParentTaskID == t.ParentTaskID && existing.DedupKey == t.DedupKey {
				return fmt.Errorf("UNIQUE constraint failed: internal_tasks.dedup_key")
			}
		}
	}
	f.tasks = append(f.tasks, *t)
	return nil
}

func (f *fakeCreator) FindChildByDedupKey(_ context.Context, parentTaskID, dedupKey string) (*model.InternalTask, error) {
	if dedupKey == "" {
		return nil, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.tasks {
		if f.tasks[i].ParentTaskID == parentTaskID && f.tasks[i].DedupKey == dedupKey {
			found := f.tasks[i]
			return &found, nil
		}
	}
	return nil, nil
}

func (f *fakeCreator) created() []model.InternalTask {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.InternalTask(nil), f.tasks...)
}

type ranWorkflow struct {
	wf   config.WorkflowConfig
	task model.InternalTask
}

func testIDGen() func(string) string {
	var seq int
	return func(p string) string { seq++; return fmt.Sprintf("%s-%d", p, seq) }
}

// TestDefaultSpawner_CreatesChildAndRuns covers 7.3.1: a spawn request creates a
// child task carrying parent_task_id + input, and the named workflow is run
// against that child.
func TestDefaultSpawner_CreatesChildAndRuns(t *testing.T) {
	creator := &fakeCreator{}
	ran := make(chan ranWorkflow, 1)
	resolve := func(id string) (config.WorkflowConfig, bool) {
		if id == "collect" {
			return config.WorkflowConfig{ID: "collect"}, true
		}
		return config.WorkflowConfig{}, false
	}
	run := func(_ context.Context, wf config.WorkflowConfig, task model.InternalTask) (bool, error) {
		ran <- ranWorkflow{wf: wf, task: task}
		return true, nil
	}
	s := NewDefaultSpawner(resolve, creator, run, testIDGen(), func() time.Time { return time.Unix(1000, 0) })

	child, err := s.Spawn(context.Background(), model.SpawnRequest{
		ParentTaskID: "parent-1",
		WorkflowID:   "collect",
		Title:        "Collect logs",
		Input:        map[string]any{"severity": "high"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if child.ParentTaskID != "parent-1" {
		t.Errorf("child ParentTaskID = %q, want parent-1", child.ParentTaskID)
	}
	if child.Title != "Collect logs" || child.Input["severity"] != "high" {
		t.Errorf("child not populated from request: %+v", child)
	}
	if child.State != model.TaskStateRegistered {
		t.Errorf("child State = %q, want registered", child.State)
	}

	created := creator.created()
	if len(created) != 1 || created[0].ID != child.ID || created[0].ParentTaskID != "parent-1" {
		t.Fatalf("child not persisted with parent: %+v", created)
	}

	select {
	case got := <-ran:
		if got.wf.ID != "collect" {
			t.Errorf("ran workflow %q, want collect", got.wf.ID)
		}
		if got.task.ID != child.ID {
			t.Errorf("ran against task %q, want child %q", got.task.ID, child.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("named workflow was not dispatched")
	}
}

// TestDefaultSpawner_MaterializeOnly covers a spawn request with no workflow: the
// child is created (carrying its labels + body) and deduped like any other, but no
// workflow is launched — the child is left for the poll loop to pick up once it is
// materialized as a source sub-issue.
func TestDefaultSpawner_MaterializeOnly(t *testing.T) {
	creator := &fakeCreator{}
	var ran int32
	resolve := func(string) (config.WorkflowConfig, bool) {
		t.Error("resolve must not be called for a materialize-only spawn")
		return config.WorkflowConfig{}, false
	}
	run := func(context.Context, config.WorkflowConfig, model.InternalTask) (bool, error) {
		atomic.AddInt32(&ran, 1)
		return true, nil
	}
	s := NewDefaultSpawner(resolve, creator, run, testIDGen(), func() time.Time { return time.Unix(1000, 0) })

	req := model.SpawnRequest{
		ParentTaskID: "parent-1",
		Title:        "Add CRUD endpoints",
		Body:         "GIVEN ... THEN ...",
		Labels:       []string{"agent:backend"},
		Key:          "customer-crud-endpoint",
	}
	child, err := s.Spawn(context.Background(), req)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if child.Description != "GIVEN ... THEN ..." {
		t.Errorf("child Description = %q, want body", child.Description)
	}
	if len(child.Metadata.Labels) != 1 || child.Metadata.Labels[0] != "agent:backend" {
		t.Errorf("child labels = %v, want [agent:backend]", child.Metadata.Labels)
	}
	if child.DedupKey != "customer-crud-endpoint" {
		t.Errorf("child DedupKey = %q, want explicit key", child.DedupKey)
	}

	// Re-running with the same key resolves to the same child (no duplicate).
	again, err := s.Spawn(context.Background(), req)
	if err != nil {
		t.Fatalf("Spawn (re-run): %v", err)
	}
	if again.ID != child.ID {
		t.Errorf("re-run created a new child %q, want existing %q", again.ID, child.ID)
	}
	if got := creator.created(); len(got) != 1 {
		t.Fatalf("materialize-only spawn persisted %d children, want 1", len(got))
	}

	// No workflow runs for a materialize-only spawn.
	time.Sleep(20 * time.Millisecond)
	if n := atomic.LoadInt32(&ran); n != 0 {
		t.Errorf("workflow ran %d times for materialize-only spawn, want 0", n)
	}
}

// TestDefaultSpawner_IdempotentPerSpec covers issue #119: re-running the same
// decomposition (same parent + workflow + title + input, or an explicit key)
// must resolve to the existing child instead of fanning out a duplicate set of
// sub-issues. The workflow is launched exactly once.
func TestDefaultSpawner_IdempotentPerSpec(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  model.SpawnRequest
	}{
		{"derived key (identical title+input)", model.SpawnRequest{
			ParentTaskID: "spec-1986",
			WorkflowID:   "implementation",
			Title:        "DB Migration: fabricantes",
			Input:        map[string]any{"task": "db", "table": "fabricantes"},
		}},
		{"explicit task_key", model.SpawnRequest{
			ParentTaskID: "spec-1986",
			WorkflowID:   "implementation",
			Title:        "Backend CRUD",
			Key:          "spec-1986/backend",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			creator := &fakeCreator{}
			var runs int32
			run := func(_ context.Context, _ config.WorkflowConfig, _ model.InternalTask) (bool, error) {
				atomic.AddInt32(&runs, 1)
				return true, nil
			}
			s := NewDefaultSpawner(
				func(string) (config.WorkflowConfig, bool) { return config.WorkflowConfig{ID: "implementation"}, true },
				creator, run, testIDGen(), func() time.Time { return time.Unix(1000, 0) },
			)

			first, err := s.Spawn(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("first Spawn: %v", err)
			}
			second, err := s.Spawn(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("second Spawn: %v", err)
			}

			if second.ID != first.ID {
				t.Errorf("second spawn created a new child %q, want existing %q", second.ID, first.ID)
			}
			if created := creator.created(); len(created) != 1 {
				t.Fatalf("created %d child tasks, want 1 (duplicate spawn not deduped)", len(created))
			}
			// Give the (single) launched workflow a moment, then assert it ran once.
			time.Sleep(50 * time.Millisecond)
			if n := atomic.LoadInt32(&runs); n != 1 {
				t.Errorf("workflow launched %d times, want 1", n)
			}
		})
	}
}

// TestDefaultSpawner_DistinctTasksNotDeduped guards against over-eager dedup:
// different tasks of the same spec (distinct titles) must each get their own
// child.
func TestDefaultSpawner_DistinctTasksNotDeduped(t *testing.T) {
	creator := &fakeCreator{}
	s := NewDefaultSpawner(
		func(string) (config.WorkflowConfig, bool) { return config.WorkflowConfig{ID: "implementation"}, true },
		creator,
		func(context.Context, config.WorkflowConfig, model.InternalTask) (bool, error) { return true, nil },
		testIDGen(), func() time.Time { return time.Unix(1000, 0) },
	)
	for _, title := range []string{"DB Migration", "Backend CRUD", "Frontend", "E2E"} {
		if _, err := s.Spawn(context.Background(), model.SpawnRequest{
			ParentTaskID: "spec-1986", WorkflowID: "implementation", Title: title,
		}); err != nil {
			t.Fatalf("Spawn %q: %v", title, err)
		}
	}
	if created := creator.created(); len(created) != 4 {
		t.Fatalf("created %d children, want 4 distinct", len(created))
	}
}

// TestDefaultSpawner_UnknownWorkflowErrors covers 7.3.4: a spawn naming an
// unknown workflow returns an error rather than silently doing nothing.
func TestDefaultSpawner_UnknownWorkflowErrors(t *testing.T) {
	creator := &fakeCreator{}
	s := NewDefaultSpawner(
		func(string) (config.WorkflowConfig, bool) { return config.WorkflowConfig{}, false },
		creator,
		func(context.Context, config.WorkflowConfig, model.InternalTask) (bool, error) { return true, nil },
		testIDGen(), func() time.Time { return time.Unix(1000, 0) },
	)

	_, err := s.Spawn(context.Background(), model.SpawnRequest{WorkflowID: "nope"})
	if err == nil {
		t.Fatal("expected error for unknown workflow, got nil")
	}
	if len(creator.created()) != 0 {
		t.Errorf("no task should be created for an unknown workflow, got %d", len(creator.created()))
	}
}

// TestDefaultSpawner_AwaitReportsOutcome verifies Await blocks until the spawned
// workflow finishes and reports the child's success (the basis for spawn: await).
func TestDefaultSpawner_AwaitReportsOutcome(t *testing.T) {
	for _, tc := range []struct {
		name    string
		success bool
		runErr  error
		want    bool
	}{
		{"child succeeds", true, nil, true},
		{"child fails", false, nil, false},
		{"run errors", true, fmt.Errorf("boom"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release := make(chan struct{})
			run := func(context.Context, config.WorkflowConfig, model.InternalTask) (bool, error) {
				<-release
				return tc.success, tc.runErr
			}
			s := NewDefaultSpawner(
				func(string) (config.WorkflowConfig, bool) { return config.WorkflowConfig{ID: "w"}, true },
				&fakeCreator{}, run, testIDGen(), func() time.Time { return time.Unix(1000, 0) },
			)
			child, err := s.Spawn(context.Background(), model.SpawnRequest{WorkflowID: "w"})
			if err != nil {
				t.Fatalf("Spawn: %v", err)
			}
			close(release)
			ok, err := s.Await(context.Background(), child.ID)
			if err != nil {
				t.Fatalf("Await: %v", err)
			}
			if ok != tc.want {
				t.Errorf("Await success = %v, want %v", ok, tc.want)
			}
		})
	}
}

func TestDefaultSpawner_AwaitUnknownTask(t *testing.T) {
	s := NewDefaultSpawner(
		func(string) (config.WorkflowConfig, bool) { return config.WorkflowConfig{}, true },
		&fakeCreator{},
		func(context.Context, config.WorkflowConfig, model.InternalTask) (bool, error) { return true, nil },
		testIDGen(), func() time.Time { return time.Unix(1000, 0) },
	)
	if _, err := s.Await(context.Background(), "never-spawned"); err == nil {
		t.Fatal("expected error awaiting an untracked task")
	}
}
