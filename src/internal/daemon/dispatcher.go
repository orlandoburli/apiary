// Package daemon contains the Apiary background dispatcher: it polls sources,
// routes cells to workers, and invokes runner adapters with concurrency control.
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/router"
	"github.com/orlandoburli/apiary/internal/runner"
	"github.com/orlandoburli/apiary/internal/source"
	"github.com/orlandoburli/apiary/internal/version"
)

// sourceStat tracks per-source poll metadata.
type sourceStat struct {
	mu        sync.Mutex
	lastPoll  time.Time
	lastCount int
	inFlight  int
}

// Dispatcher polls configured sources, routes cells to workers, and manages
// concurrent runner invocations.
type Dispatcher struct {
	cfg        *config.Config
	configFile string
	startedAt  time.Time

	router  *router.Router
	sources map[string]source.Adapter // source id → connected adapter
	runners map[string]runner.Adapter // worker id → configured runner

	sem        chan struct{} // concurrency semaphore
	active     atomic.Int32 // number of goroutines currently running
	inFlight   sync.Map     // cell id → struct{}: prevents double-dispatch
	activeRuns sync.Map     // run id → model.ActiveRun

	stats  map[string]*sourceStat // source id → stats
	statMu sync.RWMutex

	mu     sync.Mutex
	runSeq int
}

// New builds and connects a Dispatcher from the given config.
func New(ctx context.Context, cfg *config.Config, configFile string) (*Dispatcher, error) {
	r, err := router.New(cfg)
	if err != nil {
		return nil, err
	}

	d := &Dispatcher{
		cfg:        cfg,
		configFile: configFile,
		startedAt:  time.Now(),
		router:     r,
		sources:    make(map[string]source.Adapter),
		runners:    make(map[string]runner.Adapter),
		sem:        make(chan struct{}, max(cfg.Settings.Concurrency, 1)),
		stats:      make(map[string]*sourceStat),
	}

	for _, sc := range cfg.Sources {
		adapter, ok := source.New(sc.Type)
		if !ok {
			return nil, fmt.Errorf("source %q: unknown type %q", sc.ID, sc.Type)
		}
		if err := adapter.Connect(ctx, sc.Config); err != nil {
			return nil, fmt.Errorf("source %q: connect: %w", sc.ID, err)
		}
		if fs, ok := adapter.(interface {
			SetFilters(states, labels []string)
		}); ok {
			fs.SetFilters(sc.Filters.States, sc.Filters.Labels)
		}
		d.sources[sc.ID] = adapter
		d.stats[sc.ID] = &sourceStat{}
	}

	for _, wc := range cfg.Workers {
		ra, ok := runner.New(wc.Runner)
		if !ok {
			return nil, fmt.Errorf("worker %q: unknown runner type %q", wc.ID, wc.Runner)
		}
		if err := ra.Configure(workerRunConfig(wc)); err != nil {
			return nil, fmt.Errorf("worker %q: configure runner: %w", wc.ID, err)
		}
		d.runners[wc.ID] = ra
	}

	return d, nil
}

// Start launches one poll goroutine per source.
// Cancel ctx to initiate a graceful shutdown; then call wg.Wait().
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

// RunOnce polls every source once, dispatches all matching cells, waits for
// completion, and returns an error if any run failed.
func (d *Dispatcher) RunOnce(ctx context.Context) error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failedIDs []string

	for _, sc := range d.cfg.Sources {
		adapter, ok := d.sources[sc.ID]
		if !ok {
			continue
		}

		cells, err := adapter.Poll(ctx, time.Time{})
		if err != nil {
			log.Printf("[apiary] source %s: poll error: %v", sc.ID, err)
			continue
		}
		d.recordPoll(sc.ID, len(cells))

		for _, cell := range cells {
			cell := cell
			if _, loaded := d.inFlight.LoadOrStore(cell.ID, struct{}{}); loaded {
				continue
			}
			match, ok := d.router.Route(cell)
			if !ok {
				d.inFlight.Delete(cell.ID)
				continue
			}

			d.sem <- struct{}{}
			d.active.Add(1)
			wg.Add(1)

			go func() {
				defer func() {
					<-d.sem
					d.active.Add(-1)
					d.inFlight.Delete(cell.ID)
					wg.Done()
				}()
				result := d.dispatch(ctx, cell, adapter, match)
				if !result.Success {
					mu.Lock()
					failedIDs = append(failedIDs, cell.ID)
					mu.Unlock()
				}
			}()
		}
	}

	wg.Wait()

	if len(failedIDs) > 0 {
		return fmt.Errorf("%d run(s) failed: %v", len(failedIDs), failedIDs)
	}
	return nil
}

// Status returns a snapshot for the IPC status endpoint.
func (d *Dispatcher) Status() StatusResponse {
	resp := StatusResponse{
		Version:    version.Version,
		ConfigFile: d.configFile,
		Uptime:     humanDuration(time.Since(d.startedAt)),
		Concurrency: ConcurrencyStatus{
			Max:    d.cfg.Settings.Concurrency,
			Active: int(d.active.Load()),
		},
	}

	for _, sc := range d.cfg.Sources {
		st := d.getStat(sc.ID)
		ago := "never"
		if !st.lastPoll.IsZero() {
			ago = humanDuration(time.Since(st.lastPoll)) + " ago"
		}
		resp.Sources = append(resp.Sources, SourceStatus{
			ID:        sc.ID,
			Type:      sc.Type,
			LastPoll:  ago,
			LastCount: st.lastCount,
			InFlight:  st.inFlight,
		})
	}

	d.activeRuns.Range(func(_, v any) bool {
		run := v.(model.ActiveRun)
		resp.ActiveRuns = append(resp.ActiveRuns, ActiveRunStatus{
			ID:       run.ID,
			CellID:   run.Cell.ID,
			Title:    run.Cell.Title,
			WorkerID: run.WorkerID,
			Model:    run.Model,
			Status:   string(run.Status),
			Elapsed:  humanDuration(time.Since(run.StartedAt)),
		})
		return true
	})

	return resp
}

// StartServer starts an HTTP server on the Unix socket for IPC.
// It removes any stale socket file before binding.
func (d *Dispatcher) StartServer(ctx context.Context, wg *sync.WaitGroup) error {
	path := SocketPath()
	if err := ensureSocketDir(path); err != nil {
		return fmt.Errorf("socket dir: %w", err)
	}
	// remove stale socket
	_ = os.Remove(path)

	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("listening on socket %s: %w", path, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(d.Status())
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{Handler: mux}

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer os.Remove(path)
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[apiary] IPC server error: %v", err)
		}
	}()

	// shut down when context is cancelled
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	return nil
}

// pollLoop polls a single source on its configured interval.
func (d *Dispatcher) pollLoop(ctx context.Context, sc config.SourceConfig, adapter source.Adapter) {
	interval, err := sc.ParsedPollInterval()
	if err != nil {
		log.Printf("[apiary] source %s: invalid poll_interval: %v — using 60s", sc.ID, err)
		interval = 60 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastPoll time.Time
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

func (d *Dispatcher) poll(ctx context.Context, sc config.SourceConfig, adapter source.Adapter, since time.Time) {
	cells, err := adapter.Poll(ctx, since)
	if err != nil {
		log.Printf("[apiary] source %s: poll error: %v", sc.ID, err)
		return
	}
	d.recordPoll(sc.ID, len(cells))

	for _, cell := range cells {
		cell := cell
		if _, loaded := d.inFlight.LoadOrStore(cell.ID, struct{}{}); loaded {
			continue
		}
		match, ok := d.router.Route(cell)
		if !ok {
			d.inFlight.Delete(cell.ID)
			continue
		}

		log.Printf("[apiary] dispatching %s (%q) → worker %s", cell.ID, cell.Title, match.Worker.ID)

		d.sem <- struct{}{}
		d.active.Add(1)

		runID := d.nextRunID()
		d.activeRuns.Store(runID, model.ActiveRun{
			ID:        runID,
			Cell:      cell,
			WorkerID:  match.Worker.ID,
			Model:     match.Worker.Model,
			Status:    model.RunStatusRunning,
			StartedAt: time.Now(),
		})

		go func() {
			defer func() {
				<-d.sem
				d.active.Add(-1)
				d.inFlight.Delete(cell.ID)
				d.activeRuns.Delete(runID)
			}()
			d.dispatch(ctx, cell, adapter, match)
		}()
	}
}

// dispatch acknowledges, runs, and writes the result for a single cell.
func (d *Dispatcher) dispatch(ctx context.Context, cell model.Cell, adapter source.Adapter, match router.Match) model.RunResult {
	wc := match.Worker

	if d.cfg.Settings.StateLock {
		if err := adapter.Acknowledge(ctx, cell, model.AckActionInProgress); err != nil {
			log.Printf("[apiary] cell %s: acknowledge error: %v", cell.ID, err)
		}
	}

	ra, ok := d.runners[wc.ID]
	if !ok {
		log.Printf("[apiary] cell %s: runner for worker %q not found", cell.ID, wc.ID)
		return model.RunResult{WorkerID: wc.ID, Success: false}
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

	log.Printf("[apiary] cell %s: done success=%v duration=%s",
		cell.ID, result.Success, result.Duration.Round(time.Second))

	if d.cfg.Settings.ResultComment {
		if err := adapter.WriteResult(ctx, cell, result); err != nil {
			log.Printf("[apiary] cell %s: write result: %v", cell.ID, err)
		}
	}

	if match.Route.OnComplete.SetState != "" {
		if ss, ok := adapter.(source.StateSetter); ok {
			if err := ss.SetState(ctx, cell, match.Route.OnComplete.SetState); err != nil {
				log.Printf("[apiary] cell %s: set_state: %v", cell.ID, err)
			}
		}
	}

	return result
}

func (d *Dispatcher) recordPoll(sourceID string, count int) {
	d.statMu.Lock()
	defer d.statMu.Unlock()
	if st, ok := d.stats[sourceID]; ok {
		st.mu.Lock()
		st.lastPoll = time.Now()
		st.lastCount = count
		st.mu.Unlock()
	}
}

func (d *Dispatcher) getStat(sourceID string) sourceStat {
	d.statMu.RLock()
	defer d.statMu.RUnlock()
	if st, ok := d.stats[sourceID]; ok {
		st.mu.Lock()
		defer st.mu.Unlock()
		return *st
	}
	return sourceStat{}
}

func (d *Dispatcher) nextRunID() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.runSeq++
	return fmt.Sprintf("run-%04d", d.runSeq)
}

func workerRunConfig(wc config.WorkerConfig) map[string]any {
	m := map[string]any{}
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
