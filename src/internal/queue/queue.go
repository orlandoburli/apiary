// Package queue defines durable dispatch and distributed-worker contracts.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

const WorkerProtocolVersion = 1

var (
	ErrNoJob           = errors.New("no compatible queued job")
	ErrStaleClaim      = errors.New("stale or inactive job claim")
	ErrWorkerUnknown   = errors.New("worker is not registered")
	ErrWorkerDraining  = errors.New("worker is draining or not ready")
	ErrWorkerUnhealthy = errors.New("worker heartbeat is stale")
)

type JobState string

const (
	JobQueued JobState = "queued"
	// JobLeased is 'running': a lease is granted so a worker can execute the
	// job, so a leased job is work in progress, not work waiting (#465).
	JobLeased    JobState = "running"
	JobSucceeded JobState = "done"
	JobFailed    JobState = "failed"
	JobCanceled  JobState = "canceled"
)

type AttemptState string

const (
	AttemptActive    AttemptState = "active"
	AttemptSucceeded AttemptState = "succeeded"
	AttemptFailed    AttemptState = "failed"
	AttemptExpired   AttemptState = "expired"
	AttemptCanceled  AttemptState = "canceled"
)

// Job is one immutable dispatch snapshot plus its durable scheduling state.
// Payload is versioned by the control plane and contains the concrete dispatch
// request needed by a worker; the queue treats it as opaque JSON.
type Job struct {
	ID                   string          `json:"id"`
	IdempotencyKey       string          `json:"idempotency_key"`
	ProjectID            string          `json:"project_id,omitempty"`
	SourceID             string          `json:"source_id,omitempty"`
	TaskID               string          `json:"task_id,omitempty"`
	WorkflowID           string          `json:"workflow_id,omitempty"`
	AgentID              string          `json:"agent_id,omitempty"`
	RunnerID             string          `json:"runner_id,omitempty"`
	Pool                 string          `json:"pool,omitempty"`
	RequiredLabels       []string        `json:"required_labels,omitempty"`
	RequiredCapabilities []string        `json:"required_capabilities,omitempty"`
	AffinityKey          string          `json:"affinity_key,omitempty"`
	AffinityWorkerID     string          `json:"affinity_worker_id,omitempty"`
	PayloadVersion       int             `json:"payload_version"`
	Payload              json.RawMessage `json:"payload"`
	Priority             int             `json:"priority"`
	State                JobState        `json:"state"`
	AttemptCount         int             `json:"attempt_count"`
	MaxAttempts          int             `json:"max_attempts"`
	CancelRequested      bool            `json:"cancel_requested"`
	AvailableAt          time.Time       `json:"available_at"`
	LeaseExpiresAt       *time.Time      `json:"lease_expires_at,omitempty"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
}

type Attempt struct {
	ID             string       `json:"id"`
	JobID          string       `json:"job_id"`
	Number         int          `json:"number"`
	WorkerID       string       `json:"worker_id"`
	ClaimToken     string       `json:"claim_token,omitempty"`
	State          AttemptState `json:"state"`
	LeaseExpiresAt time.Time    `json:"lease_expires_at"`
	HeartbeatAt    time.Time    `json:"heartbeat_at"`
	StartedAt      time.Time    `json:"started_at"`
	FinishedAt     *time.Time   `json:"finished_at,omitempty"`
	Error          string       `json:"error,omitempty"`
}

type Claim struct {
	Job     Job     `json:"job"`
	Attempt Attempt `json:"attempt"`
}

type Worker struct {
	ID              string    `json:"id"`
	ProtocolVersion int       `json:"protocol_version"`
	Pool            string    `json:"pool,omitempty"`
	Labels          []string  `json:"labels,omitempty"`
	Capabilities    []string  `json:"capabilities,omitempty"`
	Capacity        int       `json:"capacity"`
	Ready           bool      `json:"ready"`
	Draining        bool      `json:"draining"`
	ActiveJobs      int       `json:"active_jobs"`
	LastHeartbeat   time.Time `json:"last_heartbeat"`
	RegisteredAt    time.Time `json:"registered_at"`
}

// ConcurrencyPolicy is evaluated transactionally from active leases. A zero
// limit means unlimited. Per-value maps override the corresponding default.
type ConcurrencyPolicy struct {
	DefaultProject int            `json:"default_project,omitempty"`
	Projects       map[string]int `json:"projects,omitempty"`
	DefaultSource  int            `json:"default_source,omitempty"`
	Sources        map[string]int `json:"sources,omitempty"`
	DefaultAgent   int            `json:"default_agent,omitempty"`
	Agents         map[string]int `json:"agents,omitempty"`
	DefaultRunner  int            `json:"default_runner,omitempty"`
	Runners        map[string]int `json:"runners,omitempty"`
	DefaultPool    int            `json:"default_pool,omitempty"`
	Pools          map[string]int `json:"pools,omitempty"`
}

type ClaimRequest struct {
	WorkerID      string
	LeaseDuration time.Duration
	WorkerTimeout time.Duration
	Policy        ConcurrencyPolicy
}

type HeartbeatResult struct {
	LeaseExpiresAt  time.Time `json:"lease_expires_at"`
	CancelRequested bool      `json:"cancel_requested"`
}

type FinishResult struct {
	Success bool
	Error   string
	// Note explains a successful finish that did no work — the control plane
	// decided not to execute the job (a redelivery of an already-completed
	// workflow, a `once: true` route). It is recorded on the attempt row so a
	// no-op job is distinguishable from one that really ran, without inventing a
	// queue-level state for a dispatch-level decision (issue #380).
	Note string
	// Retry is reserved for infrastructure failures where execution is safe to
	// repeat. Ordinary workflow failures are terminal at the queue layer.
	Retry   bool
	RetryAt *time.Time
}

type Store interface {
	Enqueue(context.Context, *Job) (bool, error)
	RegisterWorker(context.Context, *Worker) error
	HeartbeatWorker(context.Context, string, bool) error
	SetWorkerDrain(context.Context, string, bool) error
	Claim(context.Context, ClaimRequest) (*Claim, error)
	Heartbeat(context.Context, string, string, string, time.Duration) (*HeartbeatResult, error)
	Finish(context.Context, string, string, string, FinishResult) error
	RequestCancel(context.Context, string) error
	RequestCancelFor(context.Context, string, string) (int, error)
	ReleaseAffinity(context.Context, string) error
	ReclaimExpired(context.Context, time.Time) (int, error)
	GetJob(context.Context, string) (*Job, error)
	ListJobs(context.Context, JobState, int) ([]Job, error)
	ListWorkers(context.Context) ([]Worker, error)
}
