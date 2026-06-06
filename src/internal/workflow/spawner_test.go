package workflow

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
)

// fakeCreator records created tasks. CreateTask mimics the store's behaviour of
// leaving a caller-supplied id in place.
type fakeCreator struct {
	mu    sync.Mutex
	tasks []model.InternalTask
}

func (f *fakeCreator) CreateTask(_ context.Context, t *model.InternalTask) error {
	f.mu.Lock()
	f.tasks = append(f.tasks, *t)
	f.mu.Unlock()
	return nil
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
