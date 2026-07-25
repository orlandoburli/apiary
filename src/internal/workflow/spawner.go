package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/model"
)

// MaxSpawnDepth is the maximum number of consecutive APIARY_SPAWN hops a chain
// may take. Root tasks have depth 0; each spawn increments the depth by one. A
// request that would produce a child at depth > MaxSpawnDepth is rejected by
// DefaultSpawner.Spawn to prevent runaway spawn loops.
const MaxSpawnDepth = 5

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

// taskCreator persists a new InternalTask and looks one up by its idempotency
// key. *db.InternalTaskStore satisfies it. FindChildByDedupKey returns (nil, nil)
// when no child matches; the spawner uses it to dedup re-runs (issue #119).
type taskCreator interface {
	CreateTask(ctx context.Context, task *model.InternalTask) error
	FindChildByDedupKey(ctx context.Context, parentTaskID, dedupKey string) (*model.InternalTask, error)
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
//
// Spawning is idempotent within a parent (issue #119): each request carries a
// deterministic dedup key (req.Key, or one derived from workflow+title+input).
// If a child with that key already exists under the same parent, Spawn returns
// it without creating a duplicate or launching the workflow again — so re-running
// the same decomposition does not fan out a second, duplicate set of sub-issues.
func (s *DefaultSpawner) Spawn(ctx context.Context, req model.SpawnRequest) (model.InternalTask, error) {
	// Depth guard: reject chains that have grown too deep to prevent an injected
	// agent from creating a self-perpetuating spawn loop. Depth is set by the
	// engine (parent.Metadata.SpawnDepth+1) and never taken from agent output.
	if req.Depth > MaxSpawnDepth {
		return model.InternalTask{}, fmt.Errorf("spawn depth %d exceeds limit of %d: request rejected to prevent spawn loop", req.Depth, MaxSpawnDepth)
	}

	// A materialize-only spawn (empty workflow) creates the deduped child but runs
	// no inline workflow — the child is left for the normal poll→route loop to pick
	// up once it is materialized as a source sub-issue (see step.Materialize). Only
	// resolve (and later launch) a workflow when one is named.
	var wf config.WorkflowConfig
	if req.WorkflowID != "" {
		var ok bool
		if wf, ok = s.resolve(req.WorkflowID); !ok {
			return model.InternalTask{}, fmt.Errorf("workflow %q not found", req.WorkflowID)
		}
	}

	dedupKey := spawnDedupKey(req)

	// Idempotency check: a re-run of the same decomposition resolves to the
	// existing child rather than spawning a duplicate.
	if existing, err := s.creator.FindChildByDedupKey(ctx, req.ParentTaskID, dedupKey); err != nil {
		return model.InternalTask{}, fmt.Errorf("dedup-check spawned task: %w", err)
	} else if existing != nil {
		aplog.Info("spawn deduped: parent %s already has child %s for key %q (workflow %q) — not re-spawning",
			req.ParentTaskID, existing.ID, dedupKey, req.WorkflowID)
		s.trackExisting(*existing)
		return *existing, nil
	}

	now := s.now()
	child := model.InternalTask{
		ID:           s.newID("task"),
		ParentTaskID: req.ParentTaskID,
		Title:        req.Title,
		Description:  req.Body,
		Input:        req.Input,
		DedupKey:     dedupKey,
		State:        model.TaskStateRegistered,
		Metadata:     model.TaskMetadata{Type: "internal", Labels: req.Labels, SpawnDepth: req.Depth},
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.creator.CreateTask(ctx, &child); err != nil {
		// Lost a race against a concurrent spawn of the same key: the unique index
		// on (parent_task_id, dedup_key) rejected the insert. Resolve to the child
		// the winner created instead of surfacing the constraint error.
		if existing, ferr := s.creator.FindChildByDedupKey(ctx, req.ParentTaskID, dedupKey); ferr == nil && existing != nil {
			aplog.Info("spawn deduped (race): parent %s resolved to child %s for key %q",
				req.ParentTaskID, existing.ID, dedupKey)
			s.trackExisting(*existing)
			return *existing, nil
		}
		return model.InternalTask{}, fmt.Errorf("create spawned task: %w", err)
	}

	// Materialize-only spawn: there is no inline workflow to run or await, so return
	// the freshly created child without launching a goroutine. The child becomes a
	// real work item only once it is materialized as a source sub-issue and the
	// poll loop dispatches it (spawn: await is rejected by config lint for this
	// mode, so no caller blocks on a handle that would never close).
	if req.WorkflowID == "" {
		return child, nil
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

// trackExisting registers a completion handle for a child resolved via dedup, so
// a spawn:await caller can still observe its outcome. If the child is already
// tracked in this process (its workflow is in flight here), the live handle is
// kept. Otherwise a pre-closed handle is registered reporting success only when
// the child has already reached the done state — a deduped child is virtually
// always terminal, since the parent step that re-spawns ran after the first
// completed.
func (s *DefaultSpawner) trackExisting(child model.InternalTask) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.running[child.ID]; ok {
		return
	}
	done := make(chan struct{})
	close(done)
	s.running[child.ID] = &spawnHandle{done: done, success: child.State == model.TaskStateDone}
}

// spawnDedupKey returns the deterministic idempotency key for a spawn request,
// scoped to its parent via the (parent_task_id, dedup_key) unique index. A caller
// -supplied req.Key is used verbatim (the per-spec "task_key"). Otherwise the key
// is derived by hashing the workflow id, title, and canonical input, so two
// identical spawns (the duplicate-decomposition case) collapse to one child.
func spawnDedupKey(req model.SpawnRequest) string {
	if k := strings.TrimSpace(req.Key); k != "" {
		return k
	}
	// json.Marshal sorts map keys, so the input encoding is stable. A nil input
	// marshals to "null"; any marshal error falls back to the title alone.
	inputJSON, err := json.Marshal(req.Input)
	if err != nil {
		inputJSON = nil
	}
	h := sha256.New()
	h.Write([]byte(req.WorkflowID))
	h.Write([]byte{0x1f})
	h.Write([]byte(req.Title))
	h.Write([]byte{0x1f})
	h.Write(inputJSON)
	return "auto:" + hex.EncodeToString(h.Sum(nil))
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
