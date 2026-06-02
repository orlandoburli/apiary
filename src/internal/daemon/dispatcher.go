// Package daemon contains the Apiary background dispatcher: it polls sources,
// routes cells to workers, and invokes runner adapters with concurrency control.
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
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
	sources     map[string]source.Adapter // source id → connected adapter
	runners     map[string]runner.Adapter // worker id → configured runner
	agentRunner map[string]string         // agent id → runner type (cli, script, …)

	db       *db.Client      // SQLite database for state and logging
	logger   *logging.Logger // Structured logger (file + DB)
	retryMgr *RetryManager   // Retry logic and backoff

	sem        chan struct{} // concurrency semaphore
	active     atomic.Int32  // number of goroutines currently running
	inFlight   sync.Map      // cell id → struct{}: prevents double-dispatch
	activeRuns sync.Map      // run id → model.ActiveRun
	retryQueue sync.Map      // cell id → retryQueueEntry: cells pending retry

	stats  map[string]*sourceStat // source id → stats
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
		runners:     make(map[string]runner.Adapter),
		agentRunner: make(map[string]string),
		db:          dbClient,
		logger:      logger,
		retryMgr:    NewRetryManager(&cfg.Settings.RetryPolicy),
		sem:         make(chan struct{}, max(cfg.Settings.Concurrency, 1)),
		stats:       make(map[string]*sourceStat),
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
		if len(ac.PreferredModels) == 0 {
			return nil, fmt.Errorf("agent %q: no preferred models", ac.ID)
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
		ra, ok := runner.New(rc.Type)
		if !ok {
			return nil, fmt.Errorf("agent %q: runner type %q not found", ac.ID, rc.Type)
		}

		if err := ra.Configure(rc.Config); err != nil {
			return nil, fmt.Errorf("agent %q: configure runner: %w", ac.ID, err)
		}

		d.runners[pseudoWorkerID] = ra
		d.agentRunner[ac.ID] = rc.Type

		aplog.Info("loaded agent %s: runner=%s type=%s preferred_models=%v", ac.ID, runnerID, rc.Type, ac.PreferredModels)
	}

	// Keep legacy worker support for backward compatibility during transition
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

		aplog.Info("retrying cell %s (attempt %d)", entry.cell.ID, entry.attempt)

		d.sem <- struct{}{}
		d.active.Add(1)

		go func(e retryQueueEntry) {
			defer func() {
				<-d.sem
				d.active.Add(-1)
				d.inFlight.Delete(e.cell.ID)
			}()
			_ = d.dispatch(ctx, e.cell, e.adapter, e.match)
		}(entry)
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
	selectedModel := agent.PreferredModels[0]
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
		Timeout:      45 * time.Minute,
	}

	// Stream the runner's live output (prompt, claude conversation, stderr)
	// into the per-task log so it shows up in the dashboard in real time.
	// These are DEBUG entries — visible only when running with --debug.
	if d.logger != nil {
		cellID := cell.ID
		req.LogSink = func(e model.LogEntry) {
			switch e.Level {
			case "error":
				d.logger.TaskError(ctx, cellID, e.Message)
			case "info":
				d.logger.TaskInfo(ctx, cellID, e.Message)
			default:
				d.logger.TaskDebug(ctx, cellID, e.Message)
			}
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

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
				aplog.Warn("cell %s: assign_from_output set but no 'APIARY-ASSIGN: <agent>' directive in output", cell.ID)
				if d.logger != nil {
					d.logger.TaskError(ctx, cell.ID, "no APIARY-ASSIGN directive found in output")
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

	if oc.SetState != "" {
		if ss, ok := adapter.(source.StateSetter); ok {
			if err := ss.SetState(ctx, cell, oc.SetState); err != nil {
				aplog.Error("cell %s: set_state: %v", cell.ID, err)
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
