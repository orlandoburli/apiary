package model

import "time"

// TaskState is the lifecycle state of an InternalTask.
type TaskState string

const (
	TaskStateRegistered   TaskState = "registered"
	TaskStateRunning      TaskState = "running"
	TaskStateApprovalWait TaskState = "approval_waiting"
	TaskStateDone         TaskState = "done"
	TaskStateFailed       TaskState = "failed"
)

// InternalTask is the canonical, source-independent unit of work. It may be
// created from a source binding (see SourceBinding) or spawned by a workflow
// step (see SpawnRequest), in which case ParentTaskID and Input are populated.
type InternalTask struct {
	ID           string
	ParentTaskID string // empty if root task
	Title        string
	Description  string
	Input        map[string]any // structured input from spawner, nil for source-bound tasks
	// DedupKey makes spawning idempotent: it is a deterministic key, unique within
	// a parent, derived from the spawn request (or the caller-supplied SpawnRequest.Key).
	// A re-run of the same decomposition resolves to the existing child instead of
	// creating a duplicate. Empty for source-bound (non-spawned) tasks.
	DedupKey             string
	State                TaskState
	Metadata             TaskMetadata
	OutstandingWorkflows int
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// TaskMetadata carries routing-relevant attributes of an InternalTask. It is
// persisted as JSON in internal_tasks.metadata.
//
// For source-bound tasks the SourceBinder keeps these fields in sync with the
// live source item on every poll, so trigger matching (Router.RouteAll) sees the
// current labels/state — the apiary handoff flow mutates a source item's labels
// to advance it from one workflow to the next, and routing must observe that.
type TaskMetadata struct {
	Labels   []string
	Priority string
	Type     string // "issue", "work_item", "log_event", "internal", ...
	// Source is the originating source id for a source-bound task (empty for
	// spawned tasks). It mirrors the source item's SourceID so triggers gated on
	// match.source still resolve once routing is on the task, not the SourceItem.
	Source string
	// State mirrors the live source item's state (e.g. "todo", "in_progress")
	// so triggers gated on match.states resolve. Empty for spawned tasks.
	State string
}

// SourceBinding links a source item to an InternalTask. One task may have many
// bindings; spawned tasks may have none.
type SourceBinding struct {
	ID               string
	TaskID           string
	SourceID         string
	SourceItemID     string
	SourceItemURL    string
	SourceItemNumber string
	CreatedAt        time.Time
}

// SpawnRequest describes a new InternalTask requested by a workflow step via the
// APIARY_SPAWN marker. WorkflowSpawner (internal/workflow) consumes it to create
// the child task and, when WorkflowID is set, dispatch the named workflow. The
// JSON tags map the marker's payload ({"workflow","title","input","key","labels",
// "body"}) onto this struct; ParentTaskID is never taken from agent output — the
// engine sets it to the spawning task's id.
//
// WorkflowID is optional. When empty the spawn is "materialize-only": it creates
// the deduped child but runs no workflow, leaving the child to be picked up by the
// normal poll→route→dispatch loop once it is materialized as a source sub-issue
// (see the step's materialize: option). This is how a decomposition agent fans a
// spec out into sub-issues idempotently — re-running the agent resolves to the
// same children (issue #119) and never creates a duplicate set.
type SpawnRequest struct {
	ParentTaskID string         `json:"-"`
	WorkflowID   string         `json:"workflow"`
	Title        string         `json:"title"`
	Input        map[string]any `json:"input"`
	// Key is an optional caller-supplied idempotency key (the per-spec "task_key"):
	// when set, two spawns with the same Key under the same parent resolve to the
	// same child. When empty the spawner derives a key from (workflow, title, input)
	// so identical re-runs still dedup. See WorkflowSpawner.Spawn.
	Key string `json:"key,omitempty"`
	// Labels are applied to the source sub-issue when the child is materialized
	// (e.g. ["agent:backend"]), so the new item routes to the right workflow on the
	// next poll. Ignored when the spawn is not materialized.
	Labels []string `json:"labels,omitempty"`
	// Body is the description written to the source sub-issue when the child is
	// materialized (the spec / acceptance criteria for the downstream agent).
	Body string `json:"body,omitempty"`
}
