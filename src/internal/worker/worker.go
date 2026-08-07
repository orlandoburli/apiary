// Package worker runs protocol-1 queue workers against a queue.Store.
package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/orlandoburli/apiary/internal/queue"
)

type Executor interface {
	Execute(context.Context, queue.Job) queue.FinishResult
}

type ExecutorFunc func(context.Context, queue.Job) queue.FinishResult

func (f ExecutorFunc) Execute(ctx context.Context, job queue.Job) queue.FinishResult {
	return f(ctx, job)
}

type Config struct {
	Worker            queue.Worker
	LeaseDuration     time.Duration
	HeartbeatInterval time.Duration
	WorkerHeartbeat   time.Duration
	WorkerTimeout     time.Duration
	PollInterval      time.Duration
	Policy            queue.ConcurrencyPolicy
	OnError           func(error)
}

type Runtime struct {
	store    queue.Store
	executor Executor
	config   Config
}

func New(store queue.Store, executor Executor, config Config) (*Runtime, error) {
	if store == nil {
		return nil, fmt.Errorf("worker queue store is required")
	}
	if executor == nil {
		return nil, fmt.Errorf("worker executor is required")
	}
	if config.Worker.ID == "" {
		return nil, fmt.Errorf("worker id is required")
	}
	if config.Worker.ProtocolVersion == 0 {
		config.Worker.ProtocolVersion = queue.WorkerProtocolVersion
	}
	if config.Worker.Capacity <= 0 {
		config.Worker.Capacity = 1
	}
	config.Worker.Ready = true
	if config.LeaseDuration <= 0 {
		config.LeaseDuration = 30 * time.Second
	}
	if config.HeartbeatInterval <= 0 {
		config.HeartbeatInterval = config.LeaseDuration / 3
	}
	if config.HeartbeatInterval >= config.LeaseDuration {
		return nil, fmt.Errorf("worker heartbeat interval must be shorter than lease duration")
	}
	if config.WorkerHeartbeat <= 0 {
		config.WorkerHeartbeat = config.HeartbeatInterval
	}
	if config.WorkerTimeout <= 0 {
		config.WorkerTimeout = 30 * time.Second
	}
	if config.PollInterval <= 0 {
		config.PollInterval = 500 * time.Millisecond
	}
	return &Runtime{store: store, executor: executor, config: config}, nil
}

// Run registers the worker and claims until ctx is canceled. Cancellation begins
// graceful drain: no new claims are taken, active claims keep heartbeating and
// finish, then the worker becomes unready and Run returns.
func (r *Runtime) Run(ctx context.Context) error {
	if err := r.store.RegisterWorker(ctx, &r.config.Worker); err != nil {
		return err
	}
	heartbeatCtx, stopHeartbeat := context.WithCancel(context.Background())
	var heartbeatWG sync.WaitGroup
	heartbeatWG.Add(1)
	go func() { defer heartbeatWG.Done(); r.workerHeartbeatLoop(heartbeatCtx) }()

	var active sync.WaitGroup
	poll := time.NewTicker(r.config.PollInterval)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = r.store.SetWorkerDrain(context.Background(), r.config.Worker.ID, true)
			active.Wait()
			// Stop the heartbeat loop *before* marking the worker unready: it
			// writes ready=true on every tick, so a tick landing after the final
			// heartbeat would resurrect a drained worker as ready — leaving a row
			// that Claim accepts but no process is listening on.
			stopHeartbeat()
			heartbeatWG.Wait()
			_ = r.store.HeartbeatWorker(context.Background(), r.config.Worker.ID, false)
			return nil
		case <-poll.C:
			if _, err := r.store.ReclaimExpired(ctx, time.Now().UTC()); err != nil {
				r.report(fmt.Errorf("worker %s reclaim expired: %w", r.config.Worker.ID, err))
			}
			for {
				claim, err := r.store.Claim(ctx, queue.ClaimRequest{WorkerID: r.config.Worker.ID, LeaseDuration: r.config.LeaseDuration, WorkerTimeout: r.config.WorkerTimeout, Policy: r.config.Policy})
				if errors.Is(err, queue.ErrNoJob) {
					break
				}
				if errors.Is(err, queue.ErrWorkerDraining) || errors.Is(err, context.Canceled) {
					break
				}
				if err != nil {
					r.report(fmt.Errorf("worker %s claim: %w", r.config.Worker.ID, err))
					break
				}
				active.Add(1)
				go func() { defer active.Done(); r.execute(claim) }()
			}
		}
	}
}

func (r *Runtime) workerHeartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(r.config.WorkerHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.store.HeartbeatWorker(context.Background(), r.config.Worker.ID, true); err != nil {
				r.report(fmt.Errorf("worker %s heartbeat: %w", r.config.Worker.ID, err))
			}
		}
	}
}

func (r *Runtime) execute(claim *queue.Claim) {
	jobCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(r.config.HeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				heartbeat, err := r.store.Heartbeat(context.Background(), claim.Job.ID, claim.Attempt.ID, claim.Attempt.ClaimToken, r.config.LeaseDuration)
				if err != nil {
					r.report(fmt.Errorf("job %s heartbeat: %w", claim.Job.ID, err))
					cancel()
					return
				}
				if heartbeat.CancelRequested {
					cancel()
				}
			}
		}
	}()
	result := r.executor.Execute(jobCtx, claim.Job)
	cancel()
	close(done)
	<-heartbeatDone
	if err := r.store.Finish(context.Background(), claim.Job.ID, claim.Attempt.ID, claim.Attempt.ClaimToken, result); err != nil && !errors.Is(err, queue.ErrStaleClaim) {
		r.report(fmt.Errorf("job %s finish: %w", claim.Job.ID, err))
	}
}

func (r *Runtime) report(err error) {
	if r.config.OnError != nil {
		r.config.OnError(err)
	}
}
