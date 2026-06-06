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
	ID                   string
	ParentTaskID         string // empty if root task
	Title                string
	Description          string
	Input                map[string]any // structured input from spawner, nil for source-bound tasks
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
// the child task and dispatch the named workflow. The JSON tags map the marker's
// payload ({"workflow","title","input"}) onto this struct; ParentTaskID is never
// taken from agent output — the engine sets it to the spawning task's id.
type SpawnRequest struct {
	ParentTaskID string         `json:"-"`
	WorkflowID   string         `json:"workflow"`
	Title        string         `json:"title"`
	Input        map[string]any `json:"input"`
}
