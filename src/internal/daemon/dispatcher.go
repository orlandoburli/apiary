// Package daemon contains the Apiary background dispatcher: it polls sources,
// routes cells to workers, and invokes runner adapters with concurrency control.
package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/logging"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/router"
	runnerimpl "github.com/orlandoburli/apiary/internal/runner"
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

// retryQueueEntry tracks a cell pending retry.
type retryQueueEntry struct {
	cell       model.Cell
	adapter    source.Adapter
	match      router.Match
	retryAfter time.Time
	attempt    int
}

// Dispatcher polls configured sources, routes cells to workers, and manages
// concurrent runner invocations.
type Dispatcher struct {
	cfg        *config.Config
	configFile string
	startedAt  time.Time

	router      *router.Router
	sources     map[string]source.Adapter
	runners     map[string]runnerimpl.Runner
	agentRunner map[string]string

	db       *db.Client
	logger   *logging.Logger
	retryMgr *RetryManager

	sem       chan struct{}             // poll concurrency (size 1)
	agentSem  map[string]chan struct{} // per-agent dispatch concurrency
	active    atomic.Int32
	inFlight  sync.Map
	activeRuns sync.Map
	runCancel  sync.Map
	retryQueue sync.Map

	stats  map[string]*sourceStat
	statMu sync.RWMutex

	mu     sync.Mutex
	runSeq int
}

// New builds and connects a Dispatcher from the given config.
// Pass nil for db and logger to skip state persistence and logging to SQLite.
func New(ctx context.Context, cfg *config.Config, configFile string, dbClient *db.Client, logger *logging.Logger) (*Dispatcher, error) {
	r, err := router.New(cfg)
	if err != nil {
		return nil, err
	}

	d := &Dispatcher{
		cfg:         cfg,
		configFile:  configFile,
		startedAt:   time.Now(),
		router:      r,
		sources:     make(map[string]source.Adapter),
		runners:     make(map[string]runnerimpl.Runner),
		agentRunner: make(map[string]string),
		db:          dbClient,
		logger:      logger,
		retryMgr:    NewRetryManager(&cfg.Settings.RetryPolicy),
		sem:         make(chan struct{}, 1), // poll: one at a time
		agentSem:    make(map[string]chan struct{}),
		stats:       make(map[string]*sourceStat),
	}

	// Per-agent concurrency: each agent gets its own semaphore so that
	// e.g. 2 long-running engineers don't starve the reviewer.
	for _, ac := range cfg.Agents {
		maxW := ac.MaxWorkers
		if maxW < 1 {
			maxW = 1
		}
		d.agentSem[ac.ID] = make(chan struct{}, maxW)
	}

	for _, sc := range cfg.Sources {
		adapter, ok := source.New(sc.Type)
		if !ok {
			return nil, fmt.Errorf("source %q: unknown type %q", sc.ID, sc.Type)
		}

		// Set the source ID on the adapter if it supports it
		if si, ok := adapter.(interface {
			SetID(id string)
		}); ok {
			si.SetID(sc.ID)
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

	// Build runner ID map for lookup
	runnerMap := make(map[string]*config.RunnerConfig)
	for i, rc := range cfg.Runners {
		runnerMap[rc.ID] = &cfg.Runners[i]
	}

	// Instantiate runners from agents
	for _, ac := range cfg.Agents {
		if ac.Model == "" {
			return nil, fmt.Errorf("agent %q: model is required", ac.ID)
		}

		// Determine which runner to use: agent-specific or default
		runnerID := ac.Runner
		if runnerID == "" {
			runnerID = cfg.DefaultRunner
		}
		if runnerID == "" {
			return nil, fmt.Errorf("agent %q: no runner specified and no default_runner configured", ac.ID)
		}

		rc, ok := runnerMap[runnerID]
		if !ok {
			return nil, fmt.Errorf("agent %q: runner %q not found", ac.ID, runnerID)
		}

		// Create pseudo-worker ID for this agent
		pseudoWorkerID := fmt.Sprintf("agent-%s", ac.ID)

		// Instantiate runner of the appropriate type
		adapterName := rc.AdapterName()
		ra, ok := runnerimpl.New(adapterName)
		if !ok {
			return nil, fmt.Errorf("agent %q: runner type %q not found", ac.ID, rc.Type)
		}

		if err := ra.Configure(rc.Config); err != nil {
			return nil, fmt.Errorf("agent %q: configure runner: %w", ac.ID, err)
		}

		d.runners[pseudoWorkerID] = ra
		d.agentRunner[ac.ID] = adapterName

		aplog.Info("loaded agent %s: runner=%s type=%s provider=%s model=%s", ac.ID, runnerID, rc.Type, adapterName, ac.Model)

		if adapterName == "opencode" {
			if err := d.writeOpencodeAgent(ctx, ac, rc); err != nil {
				aplog.Warn("agent %s: write opencode agent config: %v", ac.ID, err)
			}
		}
	}

	// Keep legacy worker support for backward compatibility during transition
	for _, wc := range cfg.Workers {
		ra, ok := runnerimpl.New(wc.Runner)
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
	// A fresh process owns no in-flight runs: clear any executions left in the
	// 'running' state by a previously-killed dispatcher so the dashboard's
	// agent status reflects real, live claude processes rather than orphans.
	if d.db != nil {
		if n, err := d.db.ReconcileOrphanExecutions(ctx); err != nil {
			aplog.Warn("reconcile orphan executions: %v", err)
		} else if n > 0 {
			aplog.Info("reconciled %d orphaned running execution(s) from a previous run", n)
		}
	}

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

// DryRun polls every source once, routes cells, and prints what would be
// dispatched — without invoking any runners or modifying any task state.
func (d *Dispatcher) DryRun(ctx context.Context) error {
	total := 0
	matched := 0

	for _, sc := range d.cfg.Sources {
		adapter, ok := d.sources[sc.ID]
		if !ok {
			continue
		}

		aplog.Debug("polling source %s (dry-run)", sc.ID)
		cells, err := adapter.Poll(ctx, time.Time{})
		if err != nil {
			aplog.Error("source %s: poll error: %v", sc.ID, err)
			continue
		}
		aplog.Info("source %s: found %d cell(s)", sc.ID, len(cells))
		total += len(cells)

		for _, cell := range cells {
			m, ok := d.router.Route(cell)
			if !ok {
				aplog.Info("  %-40s  no matching route — skipped",
					truncate(cell.Title, 40))
				continue
			}
			matched++
			aplog.Info("  %-40s  → worker %-16s  model %s",
				truncate(cell.Title, 40), m.Worker.ID, m.Worker.Model)
		}
	}

	aplog.Info("dry-run complete: %d cell(s) found, %d would be dispatched", total, matched)
	return nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// processPendingRetries checks the retry queue and re-dispatches cells whose retry time has arrived.
func (d *Dispatcher) processPendingRetries(ctx context.Context) {
	if !d.retryMgr.policy.Enabled {
		return
	}

	now := time.Now()
	var readyForRetry []retryQueueEntry

	// Collect entries that are ready to retry
	d.retryQueue.Range(func(key, value any) bool {
		entry := value.(retryQueueEntry)
		if entry.retryAfter.Before(now) || entry.retryAfter.Equal(now) {
			readyForRetry = append(readyForRetry, entry)
		}
		return true
	})

	// Re-dispatch ready entries
	for _, entry := range readyForRetry {
		// Remove from queue first
		d.retryQueue.Delete(entry.cell.ID)

		agentID := entry.match.Route.Agent
		sem := d.agentSem[agentID]
		if sem != nil {
			sem <- struct{}{}
		}
		d.active.Add(1)

		go func(e retryQueueEntry, sem chan struct{}) {
			defer func() {
				if sem != nil {
					<-sem
				}
				d.active.Add(-1)
				d.inFlight.Delete(e.cell.ID)
			}()
			_ = d.dispatch(ctx, e.cell, e.adapter, e.match)
		}(entry, sem)
	}
}

// RunOnce polls every source once, dispatches all matching cells, waits for
// completion, and returns an error if any run failed.
func (d *Dispatcher) RunOnce(ctx context.Context) error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failedIDs []string

	// Process any pending retries first
	d.processPendingRetries(ctx)

	for _, sc := range d.cfg.Sources {
		adapter, ok := d.sources[sc.ID]
		if !ok {
			continue
		}

		aplog.Debug("polling source %s (once)", sc.ID)
		cells, err := adapter.Poll(ctx, time.Time{})
		if err != nil {
			aplog.Error("source %s: poll error: %v", sc.ID, err)
			continue
		}
		aplog.Info("source %s: found %d cell(s)", sc.ID, len(cells))
		d.recordPoll(sc.ID, len(cells))

		sort.Slice(cells, func(i, j int) bool {
			return cells[i].CreatedAt.Before(cells[j].CreatedAt)
		})

		for _, cell := range cells {
			cell := cell
			if _, loaded := d.inFlight.LoadOrStore(cell.ID, struct{}{}); loaded {
				continue
			}
			match, ok := d.router.Route(cell)
			if !ok {
				aplog.Debug("  %q: no route matched (source=%q labels=%v)", cell.Title, cell.SourceID, cell.Labels)
				d.inFlight.Delete(cell.ID)
				continue
			}

			agentID := match.Route.Agent
			sem := d.agentSem[agentID]
			if sem != nil {
				sem <- struct{}{}
			}
			d.active.Add(1)
			wg.Add(1)

			go func(id string, sem chan struct{}) {
				defer func() {
					if sem != nil {
						<-sem
					}
					d.active.Add(-1)
					d.inFlight.Delete(id)
					wg.Done()
				}()
				result := d.dispatch(ctx, cell, adapter, match)
				if !result.Success {
					mu.Lock()
					failedIDs = append(failedIDs, id)
					mu.Unlock()
				}
			}(cell.ID, sem)
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
	// Build per-agent concurrency status
	agentStatus := make(map[string]int)
	for _, ac := range d.cfg.Agents {
		ch := d.agentSem[ac.ID]
		used := 0
		if ch != nil {
			used = len(ch)
		}
		agentStatus[ac.ID] = used
	}

	resp := StatusResponse{
		Version:    version.Version,
		ConfigFile: d.configFile,
		Uptime:     humanDuration(time.Since(d.startedAt)),
		Concurrency: ConcurrencyStatus{
			Max:         d.cfg.Settings.Concurrency,
			Active:      int(d.active.Load()),
			AgentActive: agentStatus,
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

// ForceRestart cancels a running dispatch for the given cell, removes it from
// tracking maps, marks the execution as interrupted in the DB, and resets the
// source state so the cell can be picked up on the next poll.
func (d *Dispatcher) ForceRestart(ctx context.Context, cellID string) error {
	// Cancel the running dispatch, if any
	if val, ok := d.runCancel.LoadAndDelete(cellID); ok {
		cancel := val.(context.CancelFunc)
		cancel()
	}

	// Remove from in-flight tracking so the cell can be re-dispatched
	d.inFlight.Delete(cellID)

	// Remove from active runs
	d.activeRuns.Range(func(key, val any) bool {
		run := val.(model.ActiveRun)
		if run.Cell.ID == cellID {
			d.activeRuns.Delete(key)
		}
		return true
	})

	// Remove from in-flight so it can be re-dispatched; the agent semaphore
	// slot is released by the running goroutine's defer when the cancelled
	// context makes dispatch return.

	// Mark the running execution as interrupted in DB
	if d.db != nil {
		lastExec, err := d.db.GetLastExecution(ctx, cellID)
		if err == nil && lastExec != nil && lastExec.Status == "running" {
			now := time.Now()
			lastExec.Status = "interrupted"
			lastExec.CompletedAt = &now
			lastExec.ErrorMsg = "force-restarted by user"
			_ = d.db.UpdateExecution(ctx, lastExec)
		}

		// Reset task state so it can be re-dispatched
		_ = d.db.UpdateTaskState(ctx, cellID, "pending")
	}

	// Reset the source state (set back to todo) so the next poll picks it up
	for _, sc := range d.cfg.Sources {
		if adapter, ok := d.sources[sc.ID]; ok {
			if ss, ok := adapter.(source.StateSetter); ok {
				_ = ss.SetState(ctx, model.Cell{ID: cellID}, "todo")
			}
		}
	}

	aplog.Info("force-restarted cell %s", cellID)
	return nil
}

// StartServer starts an HTTP server on the Unix socket for IPC.
// It removes any stale socket file before binding.
func (d *Dispatcher) StartServer(ctx context.Context, wg *sync.WaitGroup) error {
	path := SocketPath(config.DataDir(d.configFile))
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
	mux.HandleFunc("/restart/", func(w http.ResponseWriter, r *http.Request) {
		cellID := strings.TrimPrefix(r.URL.Path, "/restart/")
		if cellID == "" {
			http.Error(w, "missing cell id", http.StatusBadRequest)
			return
		}
		if err := d.ForceRestart(ctx, cellID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/config/agent/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		agentID := strings.TrimPrefix(r.URL.Path, "/api/config/agent/")
		if agentID == "" {
			http.Error(w, "missing agent id", http.StatusBadRequest)
			return
		}

		var req struct {
			Model      string `json:"model,omitempty"`
			Runner     string `json:"runner,omitempty"`
			MaxWorkers int    `json:"max_workers,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}

		if err := d.UpdateAgentConfig(r.Context(), agentID, req.Model, req.Runner, req.MaxWorkers); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"updated": agentID, "model": req.Model, "max_workers": req.MaxWorkers})
	})
	mux.HandleFunc("/clearlogs/", func(w http.ResponseWriter, r *http.Request) {
		cellID := strings.TrimPrefix(r.URL.Path, "/clearlogs/")
		if cellID == "" {
			http.Error(w, "missing cell id", http.StatusBadRequest)
			return
		}
		if d.db != nil {
			if err := d.db.ClearTaskLogs(r.Context(), cellID); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{Handler: mux}

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer os.Remove(path)
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			aplog.Error("IPC server error: %v", err)
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
		aplog.Error("source %s: invalid poll_interval: %v — using 60s", sc.ID, err)
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
	aplog.Debug("polling source %s (since %s)", sc.ID, since.Format(time.RFC3339))
	cells, err := adapter.Poll(ctx, since)
	if err != nil {
		aplog.Error("source %s: poll error: %v", sc.ID, err)
		return
	}
	aplog.Info("source %s: found %d cell(s)", sc.ID, len(cells))
	d.recordPoll(sc.ID, len(cells))

	sort.Slice(cells, func(i, j int) bool {
		return cells[i].CreatedAt.Before(cells[j].CreatedAt)
	})

	for _, cell := range cells {
		cell := cell
		if _, loaded := d.inFlight.LoadOrStore(cell.ID, struct{}{}); loaded {
			aplog.Debug("cell %s: already in-flight, skipping", cell.ID)
			continue
		}
		match, ok := d.router.Route(cell)
		if !ok {
			aplog.Debug("cell %s (%q): no matching route, skipping", cell.ID, cell.Title)
			d.inFlight.Delete(cell.ID)
			continue
		}

		aplog.Info("dispatching cell %s (%q) → worker %s [%s]",
			cell.ID, cell.Title, match.Worker.ID, match.Worker.Model)

		agentID := match.Route.Agent
		agentCh := d.agentSem[agentID]
		if agentCh != nil {
			agentCh <- struct{}{}
		}
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
				if agentCh != nil {
					<-agentCh
				}
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
	// Experimental workflow mode routes the cell through the workflow engine
	// (instances + step runs + memory). The legacy path below is untouched and
	// remains the default when the flag is off.
	if d.cfg.Settings.Experimental.WorkflowMode {
		return d.dispatchWorkflow(ctx, cell, adapter, match)
	}

	// Get the agent ID from the route
	agentID := match.Route.Agent
	if agentID == "" {
		aplog.Error("cell %s: route %s has no agent", cell.ID, match.Route.ID)
		return model.RunResult{Success: false}
	}

	// Find the agent config
	var agent *config.AgentConfig
	for i := range d.cfg.Agents {
		if d.cfg.Agents[i].ID == agentID {
			agent = &d.cfg.Agents[i]
			break
		}
	}
	if agent == nil {
		aplog.Error("cell %s: agent %q not found", cell.ID, agentID)
		return model.RunResult{Success: false}
	}

	// Inject per-agent source token so the adapter (e.g. GitHub) uses the
	// agent's credentials for write operations (Acknowledge, WriteResult, etc).
	// Poll still uses the source-level token.
	if agent.SourceToken != "" {
		ctx = context.WithValue(ctx, source.SourceTokenCtxKey, agent.SourceToken)
	}

	// Log the routing decision tree: why this cell landed on this agent, and
	// which other routes were evaluated and rejected (DEBUG, per-task).
	if d.logger != nil {
		_, _, traces := d.router.Explain(cell)
		d.logger.TaskDebug(ctx, cell.ID, fmt.Sprintf(
			"routing decision for %q (source=%s labels=%v type=%s priority=%s):",
			cell.Title, cell.SourceID, cell.Labels, cell.Type, cell.Priority))
		for _, t := range traces {
			marker := "·"
			verb := "skip"
			if t.Matched {
				verb = "match"
			}
			if t.Selected {
				marker = "▶"
				verb = "SELECTED"
			}
			target := t.Agent
			if target == "" {
				target = "worker:" + t.Worker
			}
			d.logger.TaskDebug(ctx, cell.ID, fmt.Sprintf(
				"  %s route=%s (prio=%d agent=%s) %s — %s",
				marker, t.RouteID, t.Priority, target, verb, t.Reason))
		}
	}

	if d.cfg.Settings.StateLock {
		if err := adapter.Acknowledge(ctx, cell, model.AckActionInProgress); err != nil {
			aplog.Error("cell %s: acknowledge error: %v", cell.ID, err)
		}
	}

	// Use pseudo-worker ID for agent
	pseudoWorkerID := fmt.Sprintf("agent-%s", agentID)
	ra, ok := d.runners[pseudoWorkerID]
	if !ok {
		aplog.Error("cell %s: runner for agent %q not found", cell.ID, agentID)
		return model.RunResult{Success: false}
	}

	// Read soul file and append to system prompt
	systemAppend := ""
	if agent.SoulFile != "" {
		soulContent, err := os.ReadFile(agent.SoulFile)
		if err != nil {
			aplog.Error("cell %s: reading soul file %q: %v", cell.ID, agent.SoulFile, err)
		} else {
			systemAppend = string(soulContent)
			aplog.Debug("cell %s: loaded soul file (%d bytes)", cell.ID, len(soulContent))
		}
	}

	// Use first preferred model
	selectedModel := agent.Model
	runnerType := d.agentRunner[agentID]

	aplog.Info("cell %s: dispatching to agent=%q model=%s", cell.ID, agentID, selectedModel)
	if d.logger != nil {
		d.logger.TaskInfo(ctx, cell.ID, fmt.Sprintf("dispatching to agent=%s model=%s runner=%s", agentID, selectedModel, runnerType))
	}

	// Create request with default values for agent-based dispatch
	req := model.RunRequest{
		Cell:         cell,
		WorkerID:     agentID,
		Model:        selectedModel,
		MaxTurns:     15,
		SystemAppend: systemAppend,
		WorkingDir:   "/",
		Env:          map[string]string{},
		Timeout:      d.cfg.Settings.TaskTimeoutDuration(),
	}

	// Set git author identity from agent config so commits use the agent's
	// GitHub identity rather than a shared system user.
	if agent.SourceName != "" {
		req.Env["GIT_AUTHOR_NAME"] = agent.SourceName
		req.Env["GIT_COMMITTER_NAME"] = agent.SourceName
	}
	if agent.SourceEmail != "" {
		req.Env["GIT_AUTHOR_EMAIL"] = agent.SourceEmail
		req.Env["GIT_COMMITTER_EMAIL"] = agent.SourceEmail
	}

	// Stream the runner's live output (prompt, agent conversation, stderr)
	// into the per-task log (dashboard) AND to the terminal so the user can
	// watch the agent work in real time when running `apiary run`.
	if d.logger != nil {
		cellID := cell.ID
		req.LogSink = func(e model.LogEntry) {
			// Always write to DB for the dashboard
			switch e.Level {
			case "error":
				d.logger.TaskError(ctx, cellID, e.Message)
			case "info":
				d.logger.TaskInfo(ctx, cellID, e.Message)
			default:
				d.logger.TaskDebug(ctx, cellID, e.Message)
			}
			// Also print to terminal so the user sees live output
			aplog.Info("[%s] %s", cellID, e.Message)
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	d.runCancel.Store(cell.ID, cancel)
	defer func() {
		cancel()
		d.runCancel.Delete(cell.ID)
	}()

	// Track execution attempt in database
	var exec *db.Execution
	lastExec, _ := d.db.GetLastExecution(ctx, cell.ID)
	attempt := 1
	if lastExec != nil {
		attempt = lastExec.Attempt + 1
	}

	if d.db != nil {
		var err error
		exec, err = d.db.CreateExecution(ctx, cell.ID, agentID, cell.Title, cell.Number, cell.URL, selectedModel, runnerType, attempt)
		if err != nil {
			aplog.Error("cell %s: create execution record: %v", cell.ID, err)
		}
	}

	// Wire PID tracking and heartbeat into the run request so the runner can
	// report the child process PID and send periodic liveness signals.
	if exec != nil && d.db != nil {
		execID := exec.ID
		req.SetPID = func(pid int) {
			if err := d.db.SetPID(ctx, execID, pid); err != nil {
				aplog.Error("cell %s: set pid: %v", cell.ID, err)
			}
		}
		req.Heartbeat = func() {
			if err := d.db.SendHeartbeat(ctx, execID); err != nil {
				aplog.Error("cell %s: heartbeat: %v", cell.ID, err)
			}
		}
	}

	result, err := ra.Run(runCtx, req)
	if err != nil && result.Error == nil {
		result.Error = err
	}
	result.WorkerID = agentID

	aplog.Info("cell %s: done success=%v duration=%s",
		cell.ID, result.Success, result.Duration.Round(time.Second))

	// Log agent output to console
	if result.Output != "" {
		aplog.Info("cell %s: agent output:\n%s", cell.ID, result.Output)
	}
	if result.Error != nil {
		aplog.Error("cell %s: agent error: %v", cell.ID, result.Error)
	}

	// Per-task logs (visible in the dashboard Tasks → logs view)
	if d.logger != nil {
		if result.Output != "" {
			d.logger.TaskInfo(ctx, cell.ID, result.Output)
		}
		if result.Error != nil {
			d.logger.TaskError(ctx, cell.ID, result.Error.Error())
		}
		d.logger.TaskInfo(ctx, cell.ID, fmt.Sprintf("done success=%v duration=%s",
			result.Success, result.Duration.Round(time.Second)))
	}

	// Update execution record with results
	if exec != nil && d.db != nil {
		exec.Status = "success"
		exec.DurationMs = int64(result.Duration.Milliseconds())
		now := time.Now()
		exec.CompletedAt = &now
		if !result.Success {
			exec.Status = "failed"
			if result.Error != nil {
				exec.ErrorMsg = result.Error.Error()
			}
		}
		if result.Usage != nil {
			exec.InputTokens = result.Usage.InputTokens
			exec.OutputTokens = result.Usage.OutputTokens
			exec.TotalTokens = result.Usage.TotalTokens
			exec.NumTurns = result.Usage.NumTurns
			exec.NumToolCalls = result.Usage.NumToolCalls
			exec.CostUSD = result.Usage.CostUSD
		}
		_ = d.db.UpdateExecution(ctx, exec)

		// Handle retry scheduling if enabled and applicable
		if !result.Success && d.retryMgr.ShouldRetry(attempt) && d.retryMgr.IsRetriable(result.Error.Error()) {
			backoff := d.retryMgr.GetBackoffDuration(attempt)
			nextRetryAt := time.Now().Add(backoff)
			aplog.Debug("cell %s: attempt %d failed (retriable), scheduling retry in %v: %v",
				cell.ID, attempt, backoff, result.Error)
			exec.Status = "failed"
			exec.CanRetry = true
			exec.NextRetryAt = &nextRetryAt
			_ = d.db.UpdateExecution(ctx, exec)

			// Add to retry queue for processing when due
			d.retryQueue.Store(cell.ID, retryQueueEntry{
				cell:       cell,
				adapter:    adapter,
				match:      match,
				retryAfter: nextRetryAt,
				attempt:    attempt + 1,
			})
		}
	}

	if d.cfg.Settings.ResultComment {
		if err := adapter.WriteResult(ctx, cell, result); err != nil {
			aplog.Error("cell %s: write result: %v", cell.ID, err)
		}
	}

	// on_complete: apply labels (static add_labels + classifier assignment),
	// then the state transition. Only on a successful run — we don't want to
	// route a task based on a classification that failed.
	oc := match.Route.OnComplete
	if result.Success {
		labels := append([]string(nil), oc.AddLabels...)

		if oc.AssignFromOutput {
			if agentID, ok := parseAssignDirective(result.Output); ok {
				if d.agentExists(agentID) {
					prefix := oc.AssignLabelPrefix
					if prefix == "" {
						prefix = "agent:"
					}
					label := prefix + agentID
					labels = append(labels, label)
					aplog.Info("cell %s: classifier assigned agent=%s → label %q", cell.ID, agentID, label)
					if d.logger != nil {
						d.logger.TaskInfo(ctx, cell.ID, fmt.Sprintf("assigned to agent %q (label %q)", agentID, label))
					}
				} else {
					aplog.Error("cell %s: classifier chose unknown agent %q — not assigning", cell.ID, agentID)
					if d.logger != nil {
						d.logger.TaskError(ctx, cell.ID, fmt.Sprintf("classifier chose unknown agent %q", agentID))
					}
				}
			} else {
			aplog.Debug("cell %s: assign_from_output set but no 'APIARY-ASSIGN: <agent>' directive in output", cell.ID)
			if d.logger != nil {
				d.logger.TaskInfo(ctx, cell.ID, "no APIARY-ASSIGN directive found in output")
			}
			}
		}

		if len(labels) > 0 {
			if la, ok := adapter.(source.LabelAdder); ok {
				if err := la.AddLabels(ctx, cell, labels); err != nil {
					aplog.Error("cell %s: add labels %v: %v", cell.ID, labels, err)
				}
			} else {
				aplog.Error("cell %s: source does not support adding labels", cell.ID)
			}
		}
	}

	if result.Success && oc.SetState != "" {
		if ss, ok := adapter.(source.StateSetter); ok {
			if err := ss.SetState(ctx, cell, oc.SetState); err != nil {
				aplog.Error("cell %s: set_state: %v", cell.ID, err)
			}
		}
	}

	// PR review: if the cell is a pull request and the agent output includes
	// an APIARY-REVIEW directive, submit the review via the GitHub API directly.
	// This is independent of the source adapter — PR review is about code, not
	// issue tracking. The repo is parsed from the cell URL.
	if result.Success && cell.Type == "pull_request" {
		if event, _, ok := parseReviewDirective(result.Output); ok {
			token := agent.SourceToken
			if token == "" {
				token = d.sourceTokenForCell(cell)
			}
			if token != "" {
				body := fmt.Sprintf("**Apiary review by %s:**\n\n%s", agentID, result.Output)
				if err := submitPRReview(ctx, token, cell.URL, cell.ID, string(event), body); err != nil {
					aplog.Error("cell %s: submit review: %v", cell.ID, err)
				} else {
					aplog.Info("cell %s: submitted review event=%s", cell.ID, event)
					if d.logger != nil {
						d.logger.TaskInfo(ctx, cell.ID, fmt.Sprintf("submitted PR review: %s", event))
					}
				}
			}
		}
	}

	return result
}

// assignDirectiveRe matches a classifier's routing directive, e.g.
//
//	APIARY-ASSIGN: engineer
//
// anywhere in the agent's output (case-insensitive, one per line).
var assignDirectiveRe = regexp.MustCompile(`(?im)^\s*APIARY-ASSIGN:\s*(.+?)\s*$`)

// reviewDirectiveRe matches a PR review directive, e.g.
//
//	APIARY-REVIEW: approve
//	APIARY-REVIEW: request-changes
//	APIARY-REVIEW: comment
//
// The value must be one of: approve, request_changes, or comment.
var reviewDirectiveRe = regexp.MustCompile(`(?im)^\s*APIARY-REVIEW:\s*(.+?)\s*$`)

// parseAssignDirective extracts the chosen agent id from an agent's output.
// The last directive wins. A leading "agent:" on the value is tolerated and
// stripped, so both `engineer` and `agent:engineer` resolve to "engineer".
func parseAssignDirective(output string) (string, bool) {
	matches := assignDirectiveRe.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return "", false
	}
	val := strings.TrimSpace(matches[len(matches)-1][1])
	val = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(val), "agent:"))
	if val == "" {
		return "", false
	}
	return val, true
}

// parseReviewDirective extracts the PR review decision from an agent's output.
// The last directive wins. Valid values: approve, request-changes, comment.
func parseReviewDirective(output string) (string, string, bool) {
	matches := reviewDirectiveRe.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return "", "", false
	}
	val := strings.TrimSpace(strings.ToLower(matches[len(matches)-1][1]))
	return val, val, true
}

// agentExists reports whether an agent id is defined in the config.
func (d *Dispatcher) agentExists(id string) bool {
	for i := range d.cfg.Agents {
		if strings.EqualFold(d.cfg.Agents[i].ID, id) {
			return true
		}
	}
	return false
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
		return sourceStat{lastPoll: st.lastPoll, lastCount: st.lastCount, inFlight: st.inFlight}
	}
	return sourceStat{}
}

func (d *Dispatcher) nextRunID() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.runSeq++
	return fmt.Sprintf("run-%04d", d.runSeq)
}

// writeOpencodeAgent writes a markdown agent file for opencode so it can load
// the agent with the right skills, soul prompt, and permissions. The file is
// written to <working_dir>/.opencode/agents/<agent-id>.md.
func (d *Dispatcher) writeOpencodeAgent(ctx context.Context, ac config.AgentConfig, rc *config.RunnerConfig) error {
	workDir, _ := rc.Config["working_dir"].(string)
	if workDir == "" {
		return nil
	}

	agentDir := filepath.Join(workDir, ".opencode", "agents")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		return fmt.Errorf("create agent dir: %w", err)
	}

	var promptBody string
	if ac.SoulFile != "" {
		fullPath := ac.SoulFile
		if !filepath.IsAbs(fullPath) {
			fullPath = filepath.Join(filepath.Dir(d.configFile), fullPath)
		}
		data, err := os.ReadFile(fullPath)
		if err != nil {
			aplog.Warn("agent %s: read soul file %q: %v", ac.ID, ac.SoulFile, err)
		} else {
			promptBody = strings.TrimSpace(string(data))
		}
	}

	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "description: %s\n", ac.Description)
	b.WriteString("mode: primary\n")
	b.WriteString("permission:\n")
	b.WriteString("  edit: allow\n")
	b.WriteString("  bash: allow\n")
	b.WriteString("  read: allow\n")
	b.WriteString("  glob: allow\n")
	b.WriteString("  grep: allow\n")
	b.WriteString("  webfetch: allow\n")
	b.WriteString("  task: allow\n")
	b.WriteString("---\n")
	if promptBody != "" {
		b.WriteString("\n")
		b.WriteString(promptBody)
		b.WriteString("\n")
	}

	agentPath := filepath.Join(agentDir, ac.ID+".md")
	if err := os.WriteFile(agentPath, []byte(b.String()), 0644); err != nil {
		return fmt.Errorf("write agent file: %w", err)
	}
	if err := d.registerAgentInConfig(workDir, ac, agentPath); err != nil {
		aplog.Warn("agent %s: register in opencode.json: %v", ac.ID, err)
	}
	aplog.Debug("wrote opencode agent %s → %s", ac.ID, agentPath)
	return nil
}

// sourceTokenForCell returns the source-level token for the source that
// produced this cell. Used as fallback when the agent has no source_token set.
func (d *Dispatcher) sourceTokenForCell(cell model.Cell) string {
	for _, sc := range d.cfg.Sources {
		if sc.ID == cell.SourceID {
			if t, ok := sc.Config["api_key"].(string); ok {
				return t
			}
		}
	}
	return ""
}

// submitPRReview submits a pull request review via the GitHub API.
// The repo is parsed from the cell's GitHub URL (e.g.
// https://github.com/owner/repo/pull/123).
// This is independent of the source adapter — PR reviews are about code, not
// issue tracking, and may use a different token/account than the source.
func submitPRReview(ctx context.Context, token, cellURL, prNumber, event, body string) error {
	u, err := url.Parse(cellURL)
	if err != nil {
		return fmt.Errorf("parse cell URL %q: %w", cellURL, err)
	}
	parts := strings.SplitN(strings.Trim(u.Path, "/"), "/", 4)
	if len(parts) < 3 {
		return fmt.Errorf("unexpected URL path %q, expected owner/repo/pull/123", u.Path)
	}

	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%s/reviews", parts[0], parts[1], prNumber)
	payload := map[string]string{"event": event, "body": body}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal review payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create review request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("github API call: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("github API: %s: %s", resp.Status, respBody)
	}
	return nil
}

// registerAgentInConfig adds or updates the agent entry in the global opencode
// config (~/.config/opencode/opencode.json) so opencode run --agent <id> finds it.
func (d *Dispatcher) registerAgentInConfig(workDir string, ac config.AgentConfig, agentPath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("home dir: %w", err)
	}

	// Write the markdown agent file to the global agents directory
	globalAgentDir := filepath.Join(home, ".config", "opencode", "agents")
	if err := os.MkdirAll(globalAgentDir, 0755); err != nil {
		return fmt.Errorf("create global agent dir: %w", err)
	}

	// Read the markdown content we wrote to the project file
	mdData, err := os.ReadFile(agentPath)
	if err != nil {
		return fmt.Errorf("read agent file: %w", err)
	}

	globalAgentPath := filepath.Join(globalAgentDir, ac.ID+".md")
	if err := os.WriteFile(globalAgentPath, mdData, 0644); err != nil {
		return fmt.Errorf("write global agent file: %w", err)
	}

	// Update the global opencode.json with the agent entry
	globalConfigPath := filepath.Join(home, ".config", "opencode", "opencode.json")

	data, _ := os.ReadFile(globalConfigPath)
	cfg := make(map[string]any)
	if len(data) > 0 {
		_ = json.Unmarshal(data, &cfg)
	}

	// Use a relative reference from the global config directory
	agentEntry := map[string]any{
		"description": ac.Description,
		"mode":        "primary",
		"prompt":      "{file:./agents/" + ac.ID + ".md}",
		"skills":      ac.Skills,
		"permission": map[string]any{
			"edit":  "allow",
			"bash":  "allow",
			"read":  "allow",
			"glob":  "allow",
			"grep":  "allow",
			"task":  "allow",
		},
	}

	agents, _ := cfg["agent"].(map[string]any)
	if agents == nil {
		agents = make(map[string]any)
	}
	agents[ac.ID] = agentEntry
	cfg["agent"] = agents

	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(globalConfigPath, raw, 0644)
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

// UpdateAgentConfig applies a runtime change to an agent's configuration. It
// updates the in-memory Config, re-instantiates the runner if needed, resizes
// the per-agent semaphore, and persists the YAML file.
// Supported fields: model, max_workers, runner.
func (d *Dispatcher) UpdateAgentConfig(ctx context.Context, agentID, newModel, newRunner string, maxWorkers int) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	var agent *config.AgentConfig
	for i := range d.cfg.Agents {
		if d.cfg.Agents[i].ID == agentID {
			agent = &d.cfg.Agents[i]
			break
		}
	}
	if agent == nil {
		return fmt.Errorf("agent %q not found", agentID)
	}

	if newRunner != "" && newRunner != agent.Runner {
		// Look up the runner config
		var rc *config.RunnerConfig
		for i := range d.cfg.Runners {
			if d.cfg.Runners[i].ID == newRunner {
				rc = &d.cfg.Runners[i]
				break
			}
		}
		if rc == nil {
			return fmt.Errorf("runner %q not found in config runners section", newRunner)
		}

		ra, ok := runnerimpl.New(rc.Type)
		if !ok {
			return fmt.Errorf("runner type %q not registered", rc.Type)
		}
		if err := ra.Configure(rc.Config); err != nil {
			return fmt.Errorf("configure runner %q: %w", newRunner, err)
		}

		agent.Runner = newRunner
		pseudoWorkerID := fmt.Sprintf("agent-%s", agentID)
		d.runners[pseudoWorkerID] = ra
		d.agentRunner[agentID] = rc.Type

		if rc.Type == "opencode" {
			if err := d.writeOpencodeAgent(ctx, *agent, rc); err != nil {
				aplog.Warn("agent %s: write opencode agent config: %v", agentID, err)
			}
		}

		aplog.Info("agent %s: runner changed to %s (type=%s)", agentID, newRunner, rc.Type)
	}

	if maxWorkers > 0 {
		agent.MaxWorkers = maxWorkers
		d.agentSem[agentID] = make(chan struct{}, maxWorkers)
	}

	if newModel != "" {
		agent.Model = newModel
	}

	// Persist to YAML (surgical update preserving env vars)
	if d.configFile != "" {
		diff := config.AgentDiff{ID: agentID, Model: newModel, Runner: newRunner, MaxWorkers: maxWorkers}
		if err := d.cfg.ApplyAgentDiff(d.configFile, diff); err != nil {
			return fmt.Errorf("persisting config: %w", err)
		}
	}

	return nil
}


