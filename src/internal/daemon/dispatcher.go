// Package daemon contains the Apiary background dispatcher: it polls sources,
// routes cells to workers, and invokes runner adapters with concurrency control.
package daemon

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/router"
	"github.com/orlandoburli/apiary/internal/runner"
	"github.com/orlandoburli/apiary/internal/source"
)

// Dispatcher polls configured sources, routes cells to workers, and manages
// concurrent runner invocations.
type Dispatcher struct {
	cfg     *config.Config
	router  *router.Router
	sources map[string]source.Adapter  // source id → connected adapter
	runners map[string]runner.Adapter  // worker id → configured runner

	sem        chan struct{} // concurrency semaphore
	inFlight   sync.Map     // cell id → struct{}: prevents double-dispatch
	activeRuns sync.Map     // run id → model.ActiveRun

	mu      sync.Mutex
	runSeq  int // monotonic run counter for log labels
}

// New builds and connects a Dispatcher from the given config.
// It calls source.Connect() and runner.Configure() for every configured entry.
func New(ctx context.Context, cfg *config.Config) (*Dispatcher, error) {
	r, err := router.New(cfg)
	if err != nil {
		return nil, err
	}

	d := &Dispatcher{
		cfg:    cfg,
		router: r,
		sources: make(map[string]source.Adapter),
		runners: make(map[string]runner.Adapter),
		sem:    make(chan struct{}, max(cfg.Settings.Concurrency, 1)),
	}

	for _, sc := range cfg.Sources {
		adapter, ok := source.New(sc.Type)
		if !ok {
			return nil, fmt.Errorf("source %q: unknown type %q (is the adapter registered?)", sc.ID, sc.Type)
		}
		if err := adapter.Connect(ctx, sc.Config); err != nil {
			return nil, fmt.Errorf("source %q: connect: %w", sc.ID, err)
		}

		// pass filter config to adapters that support it (e.g. Plane)
		if fs, ok := adapter.(interface {
			SetFilters(states, labels []string)
		}); ok {
			fs.SetFilters(sc.Filters.States, sc.Filters.Labels)
		}

		d.sources[sc.ID] = adapter
	}

	for _, wc := range cfg.Workers {
		ra, ok := runner.New(wc.Runner)
		if !ok {
			return nil, fmt.Errorf("worker %q: unknown runner type %q (is the adapter registered?)", wc.ID, wc.Runner)
		}
		if err := ra.Configure(workerRunConfig(wc)); err != nil {
			return nil, fmt.Errorf("worker %q: configure runner: %w", wc.ID, err)
		}
		d.runners[wc.ID] = ra
	}

	return d, nil
}

// Start launches a poll goroutine per source and returns.
// Cancel ctx to initiate a graceful shutdown; then call Wait().
func (d *Dispatcher) Start(ctx context.Context, wg *sync.WaitGroup) {
	for _, sc := range d.cfg.Sources {
		sc := sc
		adapter, ok := d.sources[sc.ID]
		if !ok {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.pollLoop(ctx, sc, adapter)
		}()
	}
}

// ActiveRuns returns a snapshot of currently running dispatches.
func (d *Dispatcher) ActiveRuns() []model.ActiveRun {
	var runs []model.ActiveRun
	d.activeRuns.Range(func(_, v any) bool {
		runs = append(runs, v.(model.ActiveRun))
		return true
	})
	return runs
}

// pollLoop polls a single source on its configured interval until ctx is cancelled.
func (d *Dispatcher) pollLoop(ctx context.Context, sc config.SourceConfig, adapter source.Adapter) {
	interval, err := sc.ParsedPollInterval()
	if err != nil {
		log.Printf("[apiary] source %s: invalid poll_interval: %v", sc.ID, err)
		interval = 60 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastPoll time.Time

	// poll immediately on start
	d.poll(ctx, sc, adapter, lastPoll)
	lastPoll = time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.poll(ctx, sc, adapter, lastPoll)
			lastPoll = time.Now()
		}
	}
}

// poll fetches cells from the source and dispatches any that match a route.
func (d *Dispatcher) poll(ctx context.Context, sc config.SourceConfig, adapter source.Adapter, since time.Time) {
	cells, err := adapter.Poll(ctx, since)
	if err != nil {
		log.Printf("[apiary] source %s: poll error: %v", sc.ID, err)
		return
	}

	for _, cell := range cells {
		cell := cell

		// skip if already in flight
		if _, loaded := d.inFlight.LoadOrStore(cell.ID, struct{}{}); loaded {
			continue
		}

		match, ok := d.router.Route(cell)
		if !ok {
			d.inFlight.Delete(cell.ID)
			continue
		}

		log.Printf("[apiary] dispatching cell %s (%s) → worker %s", cell.ID, cell.Title, match.Worker.ID)

		// acquire semaphore slot (blocks if at concurrency limit)
		d.sem <- struct{}{}

		runID := d.nextRunID()
		activeRun := model.ActiveRun{
			ID:        runID,
			Cell:      cell,
			WorkerID:  match.Worker.ID,
			Model:     match.Worker.Model,
			Status:    model.RunStatusRunning,
			StartedAt: time.Now(),
		}
		d.activeRuns.Store(runID, activeRun)

		go func() {
			defer func() {
				<-d.sem
				d.inFlight.Delete(cell.ID)
				d.activeRuns.Delete(runID)
			}()
			d.dispatch(ctx, cell, adapter, match)
		}()
	}
}

// dispatch acknowledges a cell, invokes the runner, and writes the result back.
func (d *Dispatcher) dispatch(ctx context.Context, cell model.Cell, adapter source.Adapter, match router.Match) {
	wc := match.Worker

	// lock the task in the source system
	if d.cfg.Settings.StateLock {
		if err := adapter.Acknowledge(ctx, cell, model.AckActionInProgress); err != nil {
			log.Printf("[apiary] cell %s: acknowledge error: %v", cell.ID, err)
		}
	}

	ra, ok := d.runners[wc.ID]
	if !ok {
		log.Printf("[apiary] cell %s: runner for worker %q not found", cell.ID, wc.ID)
		return
	}

	req := model.RunRequest{
		Cell:         cell,
		WorkerID:     wc.ID,
		Model:        wc.Model,
		MaxTurns:     wc.Config.MaxTurns,
		SystemAppend: wc.Config.SystemAppend,
		WorkingDir:   wc.Config.WorkingDir,
		Env:          wc.Config.Env,
		Timeout:      wc.Config.ParsedTimeout(),
	}

	runCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	result, err := ra.Run(runCtx, req)
	if err != nil && result.Error == nil {
		result.Error = err
	}
	result.WorkerID = wc.ID

	log.Printf("[apiary] cell %s: run complete success=%v duration=%s",
		cell.ID, result.Success, result.Duration.Round(time.Second))

	// post result back to source
	if d.cfg.Settings.ResultComment {
		if err := adapter.WriteResult(ctx, cell, result); err != nil {
			log.Printf("[apiary] cell %s: write result error: %v", cell.ID, err)
		}
	}

	// on_complete state transition
	if match.Route.OnComplete.SetState != "" {
		if ss, ok := adapter.(source.StateSetter); ok {
			if err := ss.SetState(ctx, cell, match.Route.OnComplete.SetState); err != nil {
				log.Printf("[apiary] cell %s: on_complete set_state error: %v", cell.ID, err)
			}
		}
	}
}

func (d *Dispatcher) nextRunID() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.runSeq++
	return fmt.Sprintf("run-%04d", d.runSeq)
}

// workerRunConfig converts a WorkerConfig's runner-specific fields to the
// map[string]any that runner.Adapter.Configure() expects.
func workerRunConfig(wc config.WorkerConfig) map[string]any {
	m := map[string]any{}
	for k, v := range wc.Config.Env {
		m[k] = v
	}
	// runner-specific keys come from the raw config map if present
	if wc.RunnerConfig != nil {
		for k, v := range wc.RunnerConfig {
			m[k] = v
		}
	}
	return m
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
