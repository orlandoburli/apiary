package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/orlandoburli/apiary/internal/queue"
)

type QueueStore struct{ db *sql.DB }

func (c *Client) Queue() *QueueStore { return &QueueStore{db: c.db} }

func NewQueueStore(database *sql.DB) *QueueStore { return &QueueStore{db: database} }

func (s *QueueStore) Enqueue(ctx context.Context, job *queue.Job) (bool, error) {
	if job == nil || strings.TrimSpace(job.IdempotencyKey) == "" {
		return false, fmt.Errorf("queue job idempotency_key is required")
	}
	if job.ID == "" {
		job.ID = randomQueueID("job")
	}
	if job.PayloadVersion <= 0 {
		return false, fmt.Errorf("queue job payload_version must be positive")
	}
	if !json.Valid(job.Payload) {
		return false, fmt.Errorf("queue job payload must be valid JSON")
	}
	if job.MaxAttempts <= 0 {
		job.MaxAttempts = 3
	}
	now := time.Now().UTC()
	if job.AvailableAt.IsZero() {
		job.AvailableAt = now
	}
	labels, _ := json.Marshal(uniqueSorted(job.RequiredLabels))
	capabilities, _ := json.Marshal(uniqueSorted(job.RequiredCapabilities))
	result, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO dispatch_jobs
		  (id, idempotency_key, project_id, source_id, task_id, workflow_id, agent_id, runner_id, pool,
		   required_labels, required_capabilities, affinity_key, affinity_worker_id, payload_version, payload,
		   priority, state, attempt_count, max_attempts, cancel_requested, available_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'queued', 0, ?, 0, ?, ?, ?)
	`, job.ID, job.IdempotencyKey, nullStr(job.ProjectID), nullStr(job.SourceID), nullStr(job.TaskID), nullStr(job.WorkflowID),
		nullStr(job.AgentID), nullStr(job.RunnerID), nullStr(job.Pool), string(labels), string(capabilities), nullStr(job.AffinityKey),
		nullStr(job.AffinityWorkerID), job.PayloadVersion, string(job.Payload), job.Priority, job.MaxAttempts, job.AvailableAt.UTC(), now, now)
	if err != nil {
		return false, fmt.Errorf("enqueue dispatch job: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if rows == 0 {
		existing, getErr := s.getJobByKey(ctx, job.IdempotencyKey)
		if getErr == nil {
			*job = *existing
		}
		return false, getErr
	}
	job.State, job.AttemptCount, job.CancelRequested = queue.JobQueued, 0, false
	job.CreatedAt, job.UpdatedAt = now, now
	job.RequiredLabels, job.RequiredCapabilities = uniqueSorted(job.RequiredLabels), uniqueSorted(job.RequiredCapabilities)
	return true, nil
}

// RegisterWorker inserts or refreshes a worker registration. active_jobs is
// reset to zero on re-registration: a worker process registers once at startup
// and by definition owns no leases at that moment, so a count left behind by a
// previously-killed process is stale. Leaving it stale is silently harmful —
// Claim refuses every job while active_jobs >= capacity, and the default
// embedded-worker capacity is 1, so one orphaned lease means the restarted
// daemon leases nothing at all until the old lease times out (issue #375).
// The orphaned attempts themselves are still settled by ReclaimExpired.
func (s *QueueStore) RegisterWorker(ctx context.Context, worker *queue.Worker) error {
	if worker == nil || strings.TrimSpace(worker.ID) == "" {
		return fmt.Errorf("worker id is required")
	}
	if worker.ProtocolVersion != queue.WorkerProtocolVersion {
		return fmt.Errorf("worker %q protocol %d is incompatible; expected %d", worker.ID, worker.ProtocolVersion, queue.WorkerProtocolVersion)
	}
	if worker.Capacity <= 0 {
		return fmt.Errorf("worker %q capacity must be positive", worker.ID)
	}
	now := time.Now().UTC()
	labels, _ := json.Marshal(uniqueSorted(worker.Labels))
	capabilities, _ := json.Marshal(uniqueSorted(worker.Capabilities))
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO worker_registrations
		  (id, protocol_version, pool, labels, capabilities, capacity, ready, draining, active_jobs, last_heartbeat, registered_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET protocol_version=excluded.protocol_version, pool=excluded.pool,
		  labels=excluded.labels, capabilities=excluded.capabilities, capacity=excluded.capacity,
		  ready=excluded.ready, draining=excluded.draining, active_jobs=0,
		  last_heartbeat=excluded.last_heartbeat, updated_at=excluded.updated_at
	`, worker.ID, worker.ProtocolVersion, nullStr(worker.Pool), string(labels), string(capabilities), worker.Capacity,
		boolToInt(worker.Ready), boolToInt(worker.Draining), now, now, now)
	if err != nil {
		return fmt.Errorf("register worker %q: %w", worker.ID, err)
	}
	worker.LastHeartbeat, worker.RegisteredAt = now, now
	return nil
}

func (s *QueueStore) HeartbeatWorker(ctx context.Context, workerID string, ready bool) error {
	result, err := s.db.ExecContext(ctx, `UPDATE worker_registrations SET ready=?, last_heartbeat=?, updated_at=? WHERE id=?`, boolToInt(ready), time.Now().UTC(), time.Now().UTC(), workerID)
	if err != nil {
		return err
	}
	return requireWorkerRow(result)
}

func (s *QueueStore) SetWorkerDrain(ctx context.Context, workerID string, draining bool) error {
	ready := 1
	if draining {
		ready = 0
	}
	result, err := s.db.ExecContext(ctx, `UPDATE worker_registrations SET draining=?, ready=?, updated_at=? WHERE id=?`, boolToInt(draining), ready, time.Now().UTC(), workerID)
	if err != nil {
		return err
	}
	return requireWorkerRow(result)
}

func (s *QueueStore) Claim(ctx context.Context, request queue.ClaimRequest) (*queue.Claim, error) {
	var claim *queue.Claim
	err := retryOnBusy(ctx, func() error {
		var err error
		claim, err = s.claim(ctx, request)
		return err
	})
	return claim, err
}

func (s *QueueStore) claim(ctx context.Context, request queue.ClaimRequest) (*queue.Claim, error) {
	if request.LeaseDuration <= 0 {
		request.LeaseDuration = 30 * time.Second
	}
	if request.WorkerTimeout <= 0 {
		request.WorkerTimeout = 30 * time.Second
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	worker, err := scanWorker(tx.QueryRowContext(ctx, `SELECT id, protocol_version, COALESCE(pool,''), labels, capabilities, capacity, ready, draining, active_jobs, last_heartbeat, registered_at FROM worker_registrations WHERE id=?`, request.WorkerID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, queue.ErrWorkerUnknown
	}
	if err != nil {
		return nil, err
	}
	if !worker.Ready || worker.Draining {
		return nil, queue.ErrWorkerDraining
	}
	if worker.LastHeartbeat.Before(time.Now().UTC().Add(-request.WorkerTimeout)) {
		return nil, queue.ErrWorkerUnhealthy
	}
	if worker.ActiveJobs >= worker.Capacity {
		return nil, queue.ErrNoJob
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT id, idempotency_key, COALESCE(project_id,''), COALESCE(source_id,''), COALESCE(task_id,''),
		       COALESCE(workflow_id,''), COALESCE(agent_id,''), COALESCE(runner_id,''), COALESCE(pool,''),
		       required_labels, required_capabilities, COALESCE(affinity_key,''), COALESCE(affinity_worker_id,''),
		       payload_version, payload, priority, state, attempt_count, max_attempts, cancel_requested,
		       available_at, lease_expires_at, created_at, updated_at
		FROM dispatch_jobs
		WHERE state='queued' AND cancel_requested=0 AND available_at <= ?
		  AND (affinity_worker_id IS NULL OR affinity_worker_id='' OR affinity_worker_id=?)
		ORDER BY priority DESC, created_at ASC LIMIT 100
	`, time.Now().UTC(), worker.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var selected *queue.Job
	for rows.Next() {
		job, scanErr := scanJob(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if compatible(worker, job) {
			allowed, policyErr := withinPolicy(ctx, tx, job, request.Policy)
			if policyErr != nil {
				return nil, policyErr
			}
			if allowed {
				selected = job
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if selected == nil {
		return nil, queue.ErrNoJob
	}
	_ = rows.Close()

	now := time.Now().UTC()
	expires := now.Add(request.LeaseDuration)
	attemptID, claimToken := randomQueueID("attempt"), randomQueueID("claim")
	result, err := tx.ExecContext(ctx, `
		UPDATE dispatch_jobs SET state='leased', attempt_count=attempt_count+1,
		  lease_attempt_id=?, lease_token=?, lease_worker_id=?, lease_expires_at=?,
		  affinity_worker_id=CASE WHEN affinity_key IS NOT NULL AND affinity_key!='' AND (affinity_worker_id IS NULL OR affinity_worker_id='') THEN ? ELSE affinity_worker_id END,
		  updated_at=?
		WHERE id=? AND state='queued' AND cancel_requested=0
	`, attemptID, claimToken, worker.ID, expires, worker.ID, now, selected.ID)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, queue.ErrNoJob
	}
	selected.AttemptCount++
	_, err = tx.ExecContext(ctx, `INSERT INTO dispatch_attempts
		(id, job_id, attempt_number, worker_id, claim_token, state, lease_expires_at, heartbeat_at, started_at)
		VALUES (?, ?, ?, ?, ?, 'active', ?, ?, ?)`, attemptID, selected.ID, selected.AttemptCount, worker.ID, claimToken, expires, now, now)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE worker_registrations SET active_jobs=active_jobs+1, updated_at=? WHERE id=?`, now, worker.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	selected.State, selected.LeaseExpiresAt, selected.UpdatedAt = queue.JobLeased, &expires, now
	if selected.AffinityKey != "" && selected.AffinityWorkerID == "" {
		selected.AffinityWorkerID = worker.ID
	}
	return &queue.Claim{Job: *selected, Attempt: queue.Attempt{ID: attemptID, JobID: selected.ID, Number: selected.AttemptCount, WorkerID: worker.ID, ClaimToken: claimToken, State: queue.AttemptActive, LeaseExpiresAt: expires, HeartbeatAt: now, StartedAt: now}}, nil
}

func (s *QueueStore) Heartbeat(ctx context.Context, jobID, attemptID, token string, extension time.Duration) (*queue.HeartbeatResult, error) {
	var res *queue.HeartbeatResult
	err := retryOnBusy(ctx, func() error {
		var err error
		res, err = s.heartbeat(ctx, jobID, attemptID, token, extension)
		return err
	})
	return res, err
}

func (s *QueueStore) heartbeat(ctx context.Context, jobID, attemptID, token string, extension time.Duration) (*queue.HeartbeatResult, error) {
	if extension <= 0 {
		extension = 30 * time.Second
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	now, expires := time.Now().UTC(), time.Now().UTC().Add(extension)
	result, err := tx.ExecContext(ctx, `UPDATE dispatch_attempts SET heartbeat_at=?, lease_expires_at=? WHERE id=? AND job_id=? AND claim_token=? AND state='active'`, now, expires, attemptID, jobID, token)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, queue.ErrStaleClaim
	}
	var canceled int
	err = tx.QueryRowContext(ctx, `UPDATE dispatch_jobs SET lease_expires_at=?, updated_at=? WHERE id=? AND state='leased' AND lease_attempt_id=? AND lease_token=? RETURNING cancel_requested`, expires, now, jobID, attemptID, token).Scan(&canceled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, queue.ErrStaleClaim
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &queue.HeartbeatResult{LeaseExpiresAt: expires, CancelRequested: canceled != 0}, nil
}

// Finish is the most damaging write in the queue to lose: without it the job
// stays leased until its lease expires and is then re-run, duplicating the
// agent's work and any side effects it already performed. It retries on lock
// contention rather than surfacing the error to the worker.
func (s *QueueStore) Finish(ctx context.Context, jobID, attemptID, token string, result queue.FinishResult) error {
	return retryOnBusy(ctx, func() error { return s.finish(ctx, jobID, attemptID, token, result) })
}

func (s *QueueStore) finish(ctx context.Context, jobID, attemptID, token string, result queue.FinishResult) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var workerID string
	var attempts, maxAttempts, canceled int
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(lease_worker_id,''), attempt_count, max_attempts, cancel_requested FROM dispatch_jobs WHERE id=? AND state='leased' AND lease_attempt_id=? AND lease_token=?`, jobID, attemptID, token).Scan(&workerID, &attempts, &maxAttempts, &canceled)
	if errors.Is(err, sql.ErrNoRows) {
		return queue.ErrStaleClaim
	}
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	attemptState, jobState := queue.AttemptFailed, queue.JobFailed
	available := now
	if canceled != 0 {
		attemptState, jobState = queue.AttemptCanceled, queue.JobCanceled
	} else if result.Success {
		attemptState, jobState = queue.AttemptSucceeded, queue.JobSucceeded
	} else if result.Retry && attempts < maxAttempts {
		jobState = queue.JobQueued
		if result.RetryAt != nil {
			available = result.RetryAt.UTC()
		}
	}
	// error_message doubles as the attempt's explanation column: a failure records
	// the error, a no-op success records its Note ("skipped: …"), so a job that
	// succeeded without doing anything is distinguishable in the DB (issue #380).
	explanation := result.Error
	if explanation == "" {
		explanation = result.Note
	}
	if _, err = tx.ExecContext(ctx, `UPDATE dispatch_attempts SET state=?, finished_at=?, error_message=? WHERE id=? AND claim_token=? AND state='active'`, attemptState, now, nullStr(explanation), attemptID, token); err != nil {
		return err
	}
	terminal := any(nil)
	if jobState == queue.JobSucceeded || jobState == queue.JobFailed || jobState == queue.JobCanceled {
		terminal = now
	}
	if _, err = tx.ExecContext(ctx, `UPDATE dispatch_jobs SET state=?, available_at=?, lease_attempt_id=NULL, lease_token=NULL, lease_worker_id=NULL, lease_expires_at=NULL, terminal_at=?, updated_at=? WHERE id=? AND lease_attempt_id=? AND lease_token=?`, jobState, available, terminal, now, jobID, attemptID, token); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE worker_registrations SET active_jobs=CASE WHEN active_jobs>0 THEN active_jobs-1 ELSE 0 END, updated_at=? WHERE id=?`, now, workerID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *QueueStore) RequestCancel(ctx context.Context, jobID string) error {
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE dispatch_jobs SET cancel_requested=1, state=CASE WHEN state='queued' THEN 'canceled' ELSE state END, terminal_at=CASE WHEN state='queued' THEN ? ELSE terminal_at END, updated_at=? WHERE id=? AND state IN ('queued','leased')`, now, now, jobID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return fmt.Errorf("queue job %q is not cancelable", jobID)
	}
	return nil
}

func (s *QueueStore) RequestCancelFor(ctx context.Context, taskID, workflowID string) (int, error) {
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE dispatch_jobs SET cancel_requested=1, state=CASE WHEN state='queued' THEN 'canceled' ELSE state END, terminal_at=CASE WHEN state='queued' THEN ? ELSE terminal_at END, updated_at=? WHERE task_id=? AND workflow_id=? AND state IN ('queued','leased')`, now, now, taskID, workflowID)
	if err != nil {
		return 0, err
	}
	rows, err := result.RowsAffected()
	return int(rows), err
}

func (s *QueueStore) ReleaseAffinity(ctx context.Context, jobID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE dispatch_jobs SET affinity_worker_id=NULL, updated_at=? WHERE id=? AND state='queued'`, time.Now().UTC(), jobID)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return fmt.Errorf("queue job %q affinity can only be released while queued", jobID)
	}
	return nil
}

func (s *QueueStore) ReclaimExpired(ctx context.Context, cutoff time.Time) (int, error) {
	var n int
	err := retryOnBusy(ctx, func() error {
		var err error
		n, err = s.reclaimExpired(ctx, cutoff)
		return err
	})
	return n, err
}

func (s *QueueStore) reclaimExpired(ctx context.Context, cutoff time.Time) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT id, lease_attempt_id, COALESCE(lease_worker_id,''), attempt_count, max_attempts, cancel_requested FROM dispatch_jobs WHERE state='leased' AND lease_expires_at <= ?`, cutoff.UTC())
	if err != nil {
		return 0, err
	}
	type expired struct {
		jobID, attemptID, workerID string
		attempts, max, canceled    int
	}
	var jobs []expired
	for rows.Next() {
		var item expired
		if err := rows.Scan(&item.jobID, &item.attemptID, &item.workerID, &item.attempts, &item.max, &item.canceled); err != nil {
			rows.Close()
			return 0, err
		}
		jobs = append(jobs, item)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	for _, item := range jobs {
		if _, err := tx.ExecContext(ctx, `UPDATE dispatch_attempts SET state='expired', finished_at=?, error_message='lease expired' WHERE id=? AND state='active'`, now, item.attemptID); err != nil {
			return 0, err
		}
		state := queue.JobQueued
		terminal := any(nil)
		if item.canceled != 0 {
			state, terminal = queue.JobCanceled, now
		} else if item.attempts >= item.max {
			state, terminal = queue.JobFailed, now
		}
		if _, err := tx.ExecContext(ctx, `UPDATE dispatch_jobs SET state=?, available_at=?, lease_attempt_id=NULL, lease_token=NULL, lease_worker_id=NULL, lease_expires_at=NULL, terminal_at=?, updated_at=? WHERE id=? AND state='leased' AND lease_attempt_id=?`, state, now, terminal, now, item.jobID, item.attemptID); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE worker_registrations SET active_jobs=CASE WHEN active_jobs>0 THEN active_jobs-1 ELSE 0 END, updated_at=? WHERE id=?`, now, item.workerID); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(jobs), nil
}

func (s *QueueStore) GetJob(ctx context.Context, id string) (*queue.Job, error) {
	return scanJob(s.db.QueryRowContext(ctx, jobSelect+` WHERE id=?`, id))
}

func (s *QueueStore) ListJobs(ctx context.Context, state queue.JobState, limit int) ([]queue.Job, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	query, args := jobSelect, []any{}
	if state != "" {
		query += ` WHERE state=?`
		args = append(args, state)
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []queue.Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, *job)
	}
	return jobs, rows.Err()
}

func (s *QueueStore) getJobByKey(ctx context.Context, key string) (*queue.Job, error) {
	return scanJob(s.db.QueryRowContext(ctx, jobSelect+` WHERE idempotency_key=?`, key))
}

func (s *QueueStore) ListWorkers(ctx context.Context) ([]queue.Worker, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, protocol_version, COALESCE(pool,''), labels, capabilities, capacity, ready, draining, active_jobs, last_heartbeat, registered_at FROM worker_registrations ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var workers []queue.Worker
	for rows.Next() {
		worker, err := scanWorker(rows)
		if err != nil {
			return nil, err
		}
		workers = append(workers, *worker)
	}
	return workers, rows.Err()
}

const jobSelect = `SELECT id, idempotency_key, COALESCE(project_id,''), COALESCE(source_id,''), COALESCE(task_id,''), COALESCE(workflow_id,''), COALESCE(agent_id,''), COALESCE(runner_id,''), COALESCE(pool,''), required_labels, required_capabilities, COALESCE(affinity_key,''), COALESCE(affinity_worker_id,''), payload_version, payload, priority, state, attempt_count, max_attempts, cancel_requested, available_at, lease_expires_at, created_at, updated_at FROM dispatch_jobs`

type rowScanner interface{ Scan(...any) error }

func scanJob(row rowScanner) (*queue.Job, error) {
	var job queue.Job
	var labels, capabilities, payload string
	var canceled int
	var lease sql.NullTime
	err := row.Scan(&job.ID, &job.IdempotencyKey, &job.ProjectID, &job.SourceID, &job.TaskID, &job.WorkflowID, &job.AgentID, &job.RunnerID, &job.Pool, &labels, &capabilities, &job.AffinityKey, &job.AffinityWorkerID, &job.PayloadVersion, &payload, &job.Priority, &job.State, &job.AttemptCount, &job.MaxAttempts, &canceled, &job.AvailableAt, &lease, &job.CreatedAt, &job.UpdatedAt)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(labels), &job.RequiredLabels)
	_ = json.Unmarshal([]byte(capabilities), &job.RequiredCapabilities)
	job.Payload, job.CancelRequested = json.RawMessage(payload), canceled != 0
	if lease.Valid {
		value := lease.Time
		job.LeaseExpiresAt = &value
	}
	return &job, nil
}

func scanWorker(row rowScanner) (*queue.Worker, error) {
	var worker queue.Worker
	var labels, capabilities string
	var ready, draining int
	err := row.Scan(&worker.ID, &worker.ProtocolVersion, &worker.Pool, &labels, &capabilities, &worker.Capacity, &ready, &draining, &worker.ActiveJobs, &worker.LastHeartbeat, &worker.RegisteredAt)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(labels), &worker.Labels)
	_ = json.Unmarshal([]byte(capabilities), &worker.Capabilities)
	worker.Ready, worker.Draining = ready != 0, draining != 0
	return &worker, nil
}

func compatible(worker *queue.Worker, job *queue.Job) bool {
	if job.Pool != "" && job.Pool != worker.Pool {
		return false
	}
	return containsAll(worker.Labels, job.RequiredLabels) && containsAll(worker.Capabilities, job.RequiredCapabilities)
}

func containsAll(have, need []string) bool {
	set := map[string]bool{}
	for _, value := range have {
		set[value] = true
	}
	for _, value := range need {
		if !set[value] {
			return false
		}
	}
	return true
}

func withinPolicy(ctx context.Context, tx *sql.Tx, job *queue.Job, policy queue.ConcurrencyPolicy) (bool, error) {
	checks := []struct {
		field, value string
		limit        int
	}{
		{"project_id", job.ProjectID, scopedLimit(policy.Projects, job.ProjectID, policy.DefaultProject)},
		{"source_id", job.SourceID, scopedLimit(policy.Sources, job.SourceID, policy.DefaultSource)},
		{"agent_id", job.AgentID, scopedLimit(policy.Agents, job.AgentID, policy.DefaultAgent)},
		{"runner_id", job.RunnerID, scopedLimit(policy.Runners, job.RunnerID, policy.DefaultRunner)},
		{"pool", job.Pool, scopedLimit(policy.Pools, job.Pool, policy.DefaultPool)},
	}
	for _, check := range checks {
		if check.limit <= 0 || check.value == "" {
			continue
		}
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM dispatch_jobs WHERE state='leased' AND `+check.field+`=?`, check.value).Scan(&active); err != nil {
			return false, err
		}
		if active >= check.limit {
			return false, nil
		}
	}
	return true, nil
}

func scopedLimit(overrides map[string]int, value string, fallback int) int {
	if limit, ok := overrides[value]; ok {
		return limit
	}
	return fallback
}

func requireWorkerRow(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return queue.ErrWorkerUnknown
	}
	return nil
}

func uniqueSorted(values []string) []string {
	set := map[string]bool{}
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			set[trimmed] = true
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func randomQueueID(prefix string) string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(buffer)
}

var _ queue.Store = (*QueueStore)(nil)
