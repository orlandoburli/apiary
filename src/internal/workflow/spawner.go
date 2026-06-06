package workflow

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
)

// WorkflowSpawner creates a child InternalTask for an APIARY_SPAWN request and
// runs the named workflow against it. The engine holds one via an interface field
// so it stays testable in isolation (see Engine.spawner / WithSpawner).
type WorkflowSpawner interface {
	// Spawn creates and persists the child task, then launches the named workflow
	// against it. The run proceeds asynchronously; Spawn returns the freshly
	// created (registered) child immediately. It errors synchronously only when
	// the named workflow is unknown or persisting the task fails.
	Spawn(ctx context.Context, req model.SpawnRequest) (model.InternalTask, error)
	// Await blocks until the workflow launched for the spawned task reaches a
	// terminal state, reporting whether it succeeded. It errors if taskID was not
	// spawned by this spawner (or the wait context is cancelled).
	Await(ctx context.Context, taskID string) (bool, error)
}

// taskCreator persists a new InternalTask, filling in any unset id/timestamps.
// *db.InternalTaskStore satisfies it.
type taskCreator interface {
	CreateTask(ctx context.Context, task *model.InternalTask) error
}

// DefaultSpawner is the production WorkflowSpawner. It resolves the named
// workflow from config, persists the child task, and runs the workflow via an
// injected runner (the engine's RunInstance), tracking completion so spawn:await
// steps can block on the outcome.
type DefaultSpawner struct {
	resolve func(workflowID string) (config.WorkflowConfig, bool)
	creator taskCreator
	run     func(ctx context.Context, wf config.WorkflowConfig, task model.InternalTask) (bool, error)
	newID   func(prefix string) string
	now     func() time.Time

	mu      sync.Mutex
	running map[string]*spawnHandle // child task id → completion handle
}

// spawnHandle tracks an in-flight spawned workflow so Await can block on it.
type spawnHandle struct {
	done    chan struct{}
	success bool // valid once done is closed
}

// NewDefaultSpawner builds a DefaultSpawner. resolve maps a workflow id to its
// config (ok=false when unknown); creator persists the child task; run executes a
// workflow against a task and reports success. newID/now may be nil (defaults are
// used) — pass them in tests for determinism.
func NewDefaultSpawner(
	resolve func(string) (config.WorkflowConfig, bool),
	creator taskCreator,
	run func(context.Context, config.WorkflowConfig, model.InternalTask) (bool, error),
	newID func(string) string,
	now func() time.Time,
) *DefaultSpawner {
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = defaultIDGen(now)
	}
	return &DefaultSpawner{
		resolve: resolve,
		creator: creator,
		run:     run,
		newID:   newID,
		now:     now,
		running: map[string]*spawnHandle{},
	}
}

// Spawn resolves the named workflow, persists a child InternalTask carrying the
// parent id and input, and launches the workflow on its own goroutine. The child
// is returned immediately in the registered state; spawn:await callers block via
// Await. A missing workflow or a persistence failure is returned synchronously.
func (s *DefaultSpawner) Spawn(ctx context.Context, req model.SpawnRequest) (model.InternalTask, error) {
	wf, ok := s.resolve(req.WorkflowID)
	if !ok {
		return model.InternalTask{}, fmt.Errorf("workflow %q not found", req.WorkflowID)
	}

	now := s.now()
	child := model.InternalTask{
		ID:           s.newID("task"),
		ParentTaskID: req.ParentTaskID,
		Title:        req.Title,
		Input:        req.Input,
		State:        model.TaskStateRegistered,
		Metadata:     model.TaskMetadata{Type: "internal"},
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.creator.CreateTask(ctx, &child); err != nil {
		return model.InternalTask{}, fmt.Errorf("create spawned task: %w", err)
	}

	h := &spawnHandle{done: make(chan struct{})}
	s.mu.Lock()
	s.running[child.ID] = h
	s.mu.Unlock()

	// Fire-and-forget: the child workflow runs on its own goroutine with a context
	// detached from the parent step, so cancelling the step does not abort the
	// spawned work. spawn:await callers observe completion through Await.
	go func() {
		success, err := s.run(context.WithoutCancel(ctx), wf, child)
		h.success = success && err == nil
		close(h.done)
	}()

	return child, nil
}

// Await blocks until the workflow launched for taskID reaches a terminal state,
// returning whether it succeeded. It errors if the task was not spawned here or
// the wait context is cancelled first.
func (s *DefaultSpawner) Await(ctx context.Context, taskID string) (bool, error) {
	s.mu.Lock()
	h, ok := s.running[taskID]
	s.mu.Unlock()
	if !ok {
		return false, fmt.Errorf("spawned task %q not tracked", taskID)
	}
	select {
	case <-h.done:
		return h.success, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}
