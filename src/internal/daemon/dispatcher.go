// Package daemon contains the Apiary background dispatcher: it polls sources,
// routes cells to workers, and invokes runner adapters with concurrency control.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
	"github.com/orlandoburli/apiary/internal/workflow"
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

	router      *router.Router
	sources     map[string]source.Adapter
	runners     map[string]runnerimpl.Runner
	agentRunner map[string]string
	// agentFallbacks holds each agent's rate-limit failover chain (primary
	// excluded), in order. Pre-built at construction so failover is a cheap swap.
	agentFallbacks map[string][]runnerCandidate

	// rateLimitPaused maps a runner adapter type (e.g. "claude") to the time its
	// provider rate limit resets. While now is before that time, the dispatcher
	// routes that runner's steps to a fallback instead. Guarded by rateLimitMu.
	rateLimitPaused map[string]time.Time
	rateLimitMu     sync.Mutex

	db     *db.Client
	logger *logging.Logger

	// binder records each polled SourceItem as an InternalTask + SourceBinding.
	// nil when no DB is configured (tests / dry-run without persistence).
	binder source.SourceBinder

	sem        chan struct{}            // poll concurrency (size 1)
	agentSem   map[string]chan struct{} // per-agent dispatch concurrency
	active     atomic.Int32
	inFlight   sync.Map
	activeRuns sync.Map
	runCancel  sync.Map
	// waitAdvancing guards a parked wait_for instance while its CI re-check (and any
	// follow-on advance) is in flight, so overlapping poll cycles don't double-check
	// or double-advance the same instance. Mirrors inFlight for polled cells.
	waitAdvancing sync.Map
	// approvalAdvancing is the approval-path twin of waitAdvancing: it guards a parked
	// approval instance while its cheap re-evaluation (and any follow-on resume/abort
	// advance) is in flight, so overlapping poll cycles don't double-advance it.
	approvalAdvancing sync.Map

	stats  map[string]*sourceStat
	statMu sync.RWMutex

	mu     sync.Mutex
	runSeq int

	// workflow engine — the dispatch path. Built once and long-lived so
	// instances parked at approval steps survive across dispatch cycles.
	engine     *workflow.Engine
	engineOnce sync.Once
}

// runnerCandidate is one rung in an agent's rate-limit failover chain: a
// configured runner adapter, its adapter type (the rate-limit pause key), and
// the model to run it with.
type runnerCandidate struct {
	adapter    runnerimpl.Runner
	runnerType string
	model      string
}

// runnerPausedUntil returns the time a runner type's provider rate limit resets,
// or the zero time if it is not paused.
func (d *Dispatcher) runnerPausedUntil(runnerType string) time.Time {
	d.rateLimitMu.Lock()
	defer d.rateLimitMu.Unlock()
	return d.rateLimitPaused[runnerType]
}

// pauseRunner records that a runner type was rate-limited until `until`. A zero
// `until` (provider did not report a reset) defaults to 5 minutes out. Only
// extends an existing pause, never shortens it.
func (d *Dispatcher) pauseRunner(runnerType string, until time.Time) {
	if until.IsZero() {
		until = time.Now().Add(5 * time.Minute)
	}
	d.rateLimitMu.Lock()
	defer d.rateLimitMu.Unlock()
	if d.rateLimitPaused == nil {
		d.rateLimitPaused = make(map[string]time.Time)
	}
	if cur, ok := d.rateLimitPaused[runnerType]; !ok || until.After(cur) {
		d.rateLimitPaused[runnerType] = until
	}
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
		sources:         make(map[string]source.Adapter),
		runners:         make(map[string]runnerimpl.Runner),
		agentRunner:     make(map[string]string),
		agentFallbacks:  make(map[string][]runnerCandidate),
		rateLimitPaused: make(map[string]time.Time),
		db:          dbClient,
		logger:      logger,
		sem:         make(chan struct{}, 1), // poll: one at a time
		agentSem:    make(map[string]chan struct{}),
		stats:       make(map[string]*sourceStat),
	}

	// The binder persists InternalTasks + SourceBindings; it needs the DB.
	if dbClient != nil {
		d.binder = source.NewSourceBinder(dbClient)
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
		if js, ok := adapter.(interface {
			SetJQL(jql string)
		}); ok {
			js.SetJQL(sc.Filters.JQL)
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

		if err := ra.Configure(runnerConfigWithMCPs(rc.Config, rc.MCPs, ac.MCPs)); err != nil {
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

		// Pre-build the rate-limit failover chain so dispatch can swap runners
		// without re-instantiating adapters on the hot path.
		for _, fb := range ac.Fallbacks {
			frc, ok := runnerMap[fb.Runner]
			if !ok {
				return nil, fmt.Errorf("agent %q: fallback runner %q not found", ac.ID, fb.Runner)
			}
			fAdapterName := frc.AdapterName()
			fra, ok := runnerimpl.New(fAdapterName)
			if !ok {
				return nil, fmt.Errorf("agent %q: fallback runner type %q not found", ac.ID, fAdapterName)
			}
			if err := fra.Configure(runnerConfigWithMCPs(frc.Config, frc.MCPs, ac.MCPs)); err != nil {
				return nil, fmt.Errorf("agent %q: configure fallback runner %q: %w", ac.ID, fb.Runner, err)
			}
			if fAdapterName == "opencode" {
				if err := d.writeOpencodeAgent(ctx, ac, frc); err != nil {
					aplog.Warn("agent %s: write opencode fallback agent config: %v", ac.ID, err)
				}
			}
			d.agentFallbacks[ac.ID] = append(d.agentFallbacks[ac.ID], runnerCandidate{
				adapter:    fra,
				runnerType: fAdapterName,
				model:      fb.Model,
			})
			aplog.Info("agent %s: fallback #%d runner=%s type=%s model=%s", ac.ID, len(d.agentFallbacks[ac.ID]), fb.Runner, fAdapterName, fb.Model)
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

		// Mark any workflow instances left in the 'running' state as interrupted.
		// These are orphans from a previously-killed daemon that would otherwise
		// cause tasks to appear stuck (running for longer than daemon uptime).
		// Marking them interrupted allows the next poll to dispatch fresh instances.
		// approval_waiting instances are handled separately via rehydrateParkedApprovals.
		if n, err := d.db.ReconcileOrphanWorkflowInstances(ctx); err != nil {
			aplog.Warn("reconcile orphan workflow instances: %v", err)
		} else if n > 0 {
			aplog.Info("reconciled %d orphaned running workflow instance(s) from a previous run", n)
		}

		// Mark step_runs left non-terminal under the instances just interrupted
		// above. Without this, a step that was mid-flight when the daemon died
		// stays 'running' in the DB forever and the dashboard Task Detail view
		// renders a phantom in-progress step under an interrupted instance. Must
		// run after ReconcileOrphanWorkflowInstances so the orphaned parents are
		// already 'interrupted'.
		if n, err := d.db.ReconcileOrphanStepRuns(ctx); err != nil {
			aplog.Warn("reconcile orphan step runs: %v", err)
		} else if n > 0 {
			aplog.Info("reconciled %d orphaned step run(s) from a previous run", n)
		}
	}

	// Reconstruct workflow instances parked at an approval step into the engine's
	// in-memory parked set. That set is empty after a restart, so without this the
	// polling loop would never re-evaluate them and their tasks would be stranded
	// in 'registered' forever (the approval_waiting restart gap).
	d.rehydrateParkedApprovals(ctx)

	// Same for instances parked at a wait_for step (e.g. waiting for CI): the engine's
	// in-memory parked set is empty after a restart, so without this a poll-waiting
	// instance would never be re-checked and its task would be stranded.
	d.rehydrateParkedWaits(ctx)

	// Prune old service_logs/task_logs rows once at startup and then daily,
	// mirroring the file-side retention (settings.log_max_age_days). Without
	// this the log tables dominate apiary.db on long-lived deployments.
	if d.db != nil && d.cfg.Settings.LogMaxAgeDays > 0 {
		maxAge := time.Duration(d.cfg.Settings.LogMaxAgeDays) * 24 * time.Hour
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.pruneLogsLoop(ctx, maxAge)
		}()
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

// pruneLogsLoop deletes DB log rows older than maxAge, at startup and then
// once a day. Rows past the window stay readable in the rotated log files.
func (d *Dispatcher) pruneLogsLoop(ctx context.Context, maxAge time.Duration) {
	prune := func() {
		n, err := d.db.PruneLogsBefore(ctx, time.Now().Add(-maxAge))
		if err != nil {
			aplog.Warn("prune log rows: %v", err)
		} else if n > 0 {
			aplog.Info("pruned %d log row(s) older than %dd", n, int(maxAge.Hours()/24))
		}
	}
	prune()

	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prune()
		}
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
			task, persisted := d.bindItem(ctx, cell)
			matches := d.router.RouteAll(task)
			if len(matches) == 0 {
				aplog.Debug("  %q: no route matched (source=%q labels=%v)", cell.Title, cell.SourceID, cell.Labels)
				d.inFlight.Delete(cell.ID)
				continue
			}
			d.fanOut(ctx, cell, adapter, task, persisted, matches, &wg, func(id string) {
				mu.Lock()
				failedIDs = append(failedIDs, id)
				mu.Unlock()
			})
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

		// Mark every non-terminal workflow_instance for the cell as interrupted.
		// A daemon restart can strand a `running` (or approval_waiting/pending)
		// instance; while it stays non-terminal, dropActiveMatches shadows the
		// workflow on every poll and the cell never re-dispatches. Clearing the
		// legacy task_executions/tasks rows above is not enough — the workflow
		// engine routes on workflow_instances. Mirrors StopInstance.
		if insts, err := d.db.ListWorkflowInstancesByCell(ctx, cellID); err != nil {
			aplog.Error("force-restart %s: list workflow instances: %v", cellID, err)
		} else {
			for _, inst := range insts {
				switch inst.State {
				case db.InstanceStateRunning, db.InstanceStateApprovalWaiting, db.InstanceStateWaiting, db.InstanceStatePending:
					if err := d.db.UpdateWorkflowInstanceState(ctx, inst.ID, db.InstanceStateInterrupted); err != nil {
						aplog.Error("force-restart %s: interrupt instance %s: %v", cellID, inst.ID, err)
					} else {
						aplog.Info("force-restart %s: interrupted workflow instance %s (was %s)", cellID, inst.ID, inst.State)
					}
				}
			}
		}
	}

	// Reset the source state (set back to todo) so the next poll picks it up
	for _, sc := range d.cfg.Sources {
		if adapter, ok := d.sources[sc.ID]; ok {
			if ss, ok := adapter.(source.StateSetter); ok {
				_ = ss.SetState(ctx, model.SourceItem{ID: cellID}, "todo")
			}
		}
	}

	// Strip control labels so the cell re-enters routing from the start instead
	// of being shadowed by a stale lock (e.g. "in-progress") or a stage marker
	// (e.g. "agent:engineer"). Needs the source's current labels (TaskPoller) and
	// label removal (LabelRemover); sources missing either are skipped.
	for _, sc := range d.cfg.Sources {
		adapter, ok := d.sources[sc.ID]
		if !ok {
			continue
		}
		remover, ok := adapter.(source.LabelRemover)
		if !ok {
			continue
		}
		poller, ok := adapter.(source.TaskPoller)
		if !ok {
			continue
		}
		cell, err := poller.PollTask(ctx, cellID)
		if err != nil {
			aplog.Debug("force-restart %s: cannot fetch labels from %s: %v", cellID, sc.ID, err)
			continue
		}
		if labels := d.controlLabels(cell); len(labels) > 0 {
			if err := remover.RemoveLabels(ctx, cell, labels); err != nil {
				aplog.Error("force-restart %s: removing control labels %v: %v", cellID, labels, err)
			} else {
				aplog.Info("force-restart %s: removed control labels %v", cellID, labels)
			}
		}
	}

	aplog.Info("force-restarted cell %s", cellID)
	return nil
}

// controlLabels returns the labels on the cell that act as routing guards: any
// label matching a route's exclude_label_prefix (e.g. "agent:") or listed in a
// route's exclude_labels (e.g. "in-progress"). These are exactly the labels that
// can keep a cell from matching a route, so force-restart strips them to send the
// cell back to the start of the flow. Derived from the live config — no
// hardcoded label names.
func (d *Dispatcher) controlLabels(cell model.SourceItem) []string {
	var prefixes []string
	excluded := map[string]bool{}
	collect := func(m config.RouteMatch) {
		if m.ExcludeLabelPrefix != "" {
			prefixes = append(prefixes, strings.ToLower(m.ExcludeLabelPrefix))
		}
		for _, l := range m.ExcludeLabels {
			excluded[strings.ToLower(l)] = true
		}
	}
	for _, wf := range d.cfg.Workflows {
		if wf.Trigger != nil {
			collect(wf.Trigger.Match)
		}
	}

	var out []string
	for _, l := range cell.Labels {
		ll := strings.ToLower(l)
		if excluded[ll] {
			out = append(out, l)
			continue
		}
		for _, p := range prefixes {
			if strings.HasPrefix(ll, p) {
				out = append(out, l)
				break
			}
		}
	}
	return out
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
	mux.HandleFunc("/instances", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit := 20
		if l := q.Get("limit"); l != "" {
			if n, err := strconv.Atoi(l); err == nil && n > 0 {
				limit = n
			}
		}
		resp, err := d.Instances(r.Context(), q.Get("state"), q.Get("workflow"), limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/instances/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/instances/")
		if id == "" {
			http.Error(w, "missing instance id", http.StatusBadRequest)
			return
		}
		detail, err := d.InstanceDetail(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if detail == nil {
			http.Error(w, "instance not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(detail)
	})
	mux.HandleFunc("/tasks/history", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Either ?task=<internal-task-id> directly, or ?source=<id>&item=<num>
		// (e.g. github + 1948) which is resolved to the bound task.
		taskID := r.URL.Query().Get("task")
		if taskID == "" {
			source, item := r.URL.Query().Get("source"), r.URL.Query().Get("item")
			if source == "" || item == "" {
				http.Error(w, "provide ?task=<id> or ?source=<id>&item=<num>", http.StatusBadRequest)
				return
			}
			if d.db == nil {
				http.Error(w, "no database", http.StatusServiceUnavailable)
				return
			}
			binding, err := d.db.SourceBindings().GetBindingBySourceItem(r.Context(), source, item)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if binding == nil {
				http.Error(w, "no task bound to "+source+"/"+item, http.StatusNotFound)
				return
			}
			taskID = binding.TaskID
		}
		hist, err := d.TaskHistory(r.Context(), taskID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if hist == nil {
			http.Error(w, "task history not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(hist)
	})
	mux.HandleFunc("/tasks/pulls/refresh/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ref := strings.TrimPrefix(r.URL.Path, "/tasks/pulls/refresh/")
		if ref == "" {
			http.Error(w, "missing task ref", http.StatusBadRequest)
			return
		}
		resp, err := d.RefreshTaskPullRequests(r.Context(), ref)
		if err != nil {
			if errors.Is(err, ErrTaskNotFound) {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/resume/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/resume/")
		if id == "" {
			// `apiary resume --workflow <id>`: resolve the most recent
			// resumable instance for the workflow.
			wfID := r.URL.Query().Get("workflow")
			if wfID == "" {
				http.Error(w, "missing instance id", http.StatusBadRequest)
				return
			}
			instID, err := d.ResolveResumeTarget(r.Context(), wfID)
			if err != nil {
				http.Error(w, err.Error(), resumeHTTPStatus(err))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"instance_id": instID})
			return
		}
		switch r.Method {
		case http.MethodGet:
			preview, err := d.ResumePreview(r.Context(), id)
			if err != nil {
				http.Error(w, err.Error(), resumeHTTPStatus(err))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(preview)
		case http.MethodPost:
			// Launch on the daemon-lifetime ctx so the run outlives the request.
			if err := d.StartResume(ctx, id); err != nil {
				http.Error(w, err.Error(), resumeHTTPStatus(err))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"queued": true, "instance_id": id})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/workflows", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(d.WorkflowList())
	})
	mux.HandleFunc("/instances/stop/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		instanceID := strings.TrimPrefix(r.URL.Path, "/instances/stop/")
		if instanceID == "" {
			http.Error(w, "missing instance id", http.StatusBadRequest)
			return
		}
		if err := d.StopInstance(ctx, instanceID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"stopped": instanceID})
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
	mux.HandleFunc("/tasks/delete/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		taskRef := strings.TrimPrefix(r.URL.Path, "/tasks/delete/")
		if taskRef == "" {
			http.Error(w, "missing task reference", http.StatusBadRequest)
			return
		}
		if err := d.DeleteTask(ctx, taskRef); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, ErrTaskNotFound) {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"deleted": taskRef})
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
	// Re-evaluate any workflows parked at approval steps against their live tasks
	// on each poll cycle (resume/abort/timeout) before fetching new work.
	d.checkApprovals(ctx)

	// Re-check any workflows parked at wait_for steps (e.g. waiting for CI) so they
	// advance, fail, or keep waiting based on live status.
	d.checkWaits(ctx)

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
		task, persisted := d.bindItem(ctx, cell)
		matches := d.router.RouteAll(task)
		// Source-agnostic in-flight guard: drop any matched workflow that already
		// has a non-terminal instance for this task, so a re-poll does not dispatch
		// a duplicate while it runs or waits at an approval step. Replaces the
		// label-based lock (state_lock / "in-progress"), which is source-specific.
		if persisted && d.db != nil {
			matches = d.dropActiveMatches(ctx, task.ID, matches)
			matches = d.dropOnceMatches(ctx, task.ID, matches)
			matches = d.dropCappedMatches(ctx, task, matches)
		}
		if len(matches) == 0 {
			aplog.Debug("cell %s (%q): no matching route, skipping", cell.ID, cell.Title)
			d.inFlight.Delete(cell.ID)
			continue
		}
		d.fanOut(ctx, cell, adapter, task, persisted, matches, nil, nil)
	}
}

// fanOut dispatches every workflow matched for a polled cell. One InternalTask
// may match several triggers, so the cell fans out to N workflows. The task's
// outstanding-workflow counter is bumped by len(matches) before any workflow
// starts (so a completion hook can tell when all have finished); then one
// dispatch goroutine is launched per match, each admitted through its agent's
// semaphore and tracked as an active run. The cell's in-flight marker is cleared
// only after every dispatch finishes. When wg is non-nil each dispatch joins it
// (so RunOnce can wait); onFail, if set, is called with the cell ID per failure.
func (d *Dispatcher) fanOut(ctx context.Context, cell model.SourceItem, adapter source.Adapter, task model.InternalTask, persisted bool, matches []router.Match, wg *sync.WaitGroup, onFail func(cellID string)) {
	if len(matches) == 0 {
		d.inFlight.Delete(cell.ID)
		return
	}

	// Track outstanding workflows on the task up front, before any can complete.
	if persisted && d.db != nil {
		if _, err := d.db.InternalTasks().IncrementOutstanding(ctx, task.ID, len(matches)); err != nil {
			aplog.Error("task %s: increment outstanding by %d: %v", task.ID, len(matches), err)
		}
	}

	var inner sync.WaitGroup
	for _, match := range matches {
		match := match
		agentCh := d.agentSem[match.Route.Agent]
		inner.Add(1)
		if wg != nil {
			wg.Add(1)
		}
		runID := d.nextRunID()

		go func(runID string, match router.Match, agentCh chan struct{}) {
			if wg != nil {
				defer wg.Done()
			}
			defer inner.Done()

			// Admit through the agent's semaphore HERE, inside the goroutine,
			// rather than on the poll-loop thread. A saturated agent must not block
			// the poll loop — doing so stalls polling and dispatch for every other
			// source and agent. A run waiting for a slot is not yet counted active.
			if agentCh != nil {
				select {
				case agentCh <- struct{}{}:
				case <-ctx.Done():
					return // shutting down before a slot freed; never acquired
				}
				defer func() { <-agentCh }()
			}

			d.active.Add(1)
			defer d.active.Add(-1)
			d.activeRuns.Store(runID, model.ActiveRun{
				ID:        runID,
				Cell:      cell,
				WorkerID:  match.Worker.ID,
				Model:     match.Worker.Model,
				Status:    model.RunStatusRunning,
				StartedAt: time.Now(),
			})
			defer d.activeRuns.Delete(runID)

			aplog.Info("dispatching cell %s (%q) → workflow %s [agent %s]",
				cell.ID, cell.Title, match.Route.ID, match.Route.Agent)

			result := d.dispatch(ctx, cell, adapter, task, match)
			if !result.Success && onFail != nil {
				onFail(cell.ID)
			}
		}(runID, match, agentCh)
	}

	// Release the in-flight marker once all of this cell's dispatches finish, so
	// the cell can be re-polled (and re-routed against its refreshed task).
	go func() {
		inner.Wait()
		d.inFlight.Delete(cell.ID)
	}()
}

// transientTask builds an unpersisted InternalTask from a source item, mapping
// the same routing-relevant attributes the SourceBinder would. It is the routing
// target when no binder is configured (no DB) — routing still works, but there is
// no task ID to track outstanding workflows against.
func transientTask(cell model.SourceItem) model.InternalTask {
	return model.InternalTask{
		// No DB row exists, so this id is never persisted; it stands in as the
		// engine's execution-view id (sourceItemView falls back to it when the
		// task has no binding) so the runner still keys on the source item id.
		ID:          cell.ID,
		Title:       cell.Title,
		Description: cell.Description,
		State:       model.TaskStateRegistered,
		Metadata: model.TaskMetadata{
			Labels:   cell.Labels,
			Priority: cell.Priority,
			Type:     cell.Type,
			Source:   cell.SourceID,
			State:    cell.State,
		},
	}
}

// bindItem records a polled source item as an InternalTask + SourceBinding via the
// binder and returns the task to route on. It is idempotent: re-polling the same
// item resolves to the same task, refreshed from the live item. With no binder (no
// DB) it returns a transient, unpersisted task built from the item so routing
// still works. persisted reports whether the task is DB-backed — only then can
// outstanding_workflows be tracked. A bind failure is logged, not fatal: routing
// falls back to a transient task.
func (d *Dispatcher) bindItem(ctx context.Context, cell model.SourceItem) (task model.InternalTask, persisted bool) {
	if d.binder == nil {
		return transientTask(cell), false
	}
	task, err := d.binder.Bind(ctx, cell)
	if err != nil {
		aplog.Error("bind source item %s (%q): %v", cell.ID, cell.Title, err)
		return transientTask(cell), false
	}
	aplog.Debug("bound source item %s → task %s [%s]", cell.ID, task.ID, task.State)
	return task, true
}

// dropActiveMatches removes matches whose workflow already has a non-terminal
// instance for this task (running or approval_waiting). It is the source-agnostic
// in-flight guard: the in-memory inFlight marker is released when a workflow parks
// at an approval step, so without this a later poll would dispatch a duplicate
// while the instance is still waiting. Keyed on (task, workflow) so a completed
// earlier workflow (e.g. triage) does not block the one a hand-off routes to.
// Fail-closed: on a query error the match is dropped (skip this poll, retry next)
// rather than risk a duplicate dispatch.
func (d *Dispatcher) dropActiveMatches(ctx context.Context, taskID string, matches []router.Match) []router.Match {
	out := make([]router.Match, 0, len(matches))
	for _, m := range matches {
		active, err := d.db.HasActiveInstanceForRoute(ctx, taskID, m.Route.ID)
		if err != nil {
			aplog.Error("in-flight check task %s workflow %s: %v — skipping this poll", taskID, m.Route.ID, err)
			continue
		}
		if active {
			aplog.Debug("task %s: workflow %s already active (running/approval_waiting), skipping re-dispatch", taskID, m.Route.ID)
			continue
		}
		out = append(out, m)
	}
	return out
}

// dropOnceMatches removes matches whose trigger declared `once: true` and that
// already have a completed (done) instance for this task. It is the source-agnostic
// run-at-most-once guard: a decomposition / fan-out workflow whose source item (a
// spec issue) stays in its trigger set after succeeding would otherwise be
// re-dispatched on every poll, fanning out a duplicate set of children (issue
// #119). Only routes that opt in via `once` are checked; all others pass through.
// Fail-closed: on a query error the (once) match is dropped — skip this poll and
// retry next, rather than risk a duplicate fan-out.
func (d *Dispatcher) dropOnceMatches(ctx context.Context, taskID string, matches []router.Match) []router.Match {
	out := make([]router.Match, 0, len(matches))
	for _, m := range matches {
		if !m.Route.Once {
			out = append(out, m)
			continue
		}
		done, err := d.db.HasCompletedInstanceForRoute(ctx, taskID, m.Route.ID)
		if err != nil {
			aplog.Error("once check task %s workflow %s: %v — skipping this poll", taskID, m.Route.ID, err)
			continue
		}
		if done {
			aplog.Debug("task %s: workflow %s already completed and is once-only, skipping re-dispatch", taskID, m.Route.ID)
			continue
		}
		out = append(out, m)
	}
	return out
}

// dropCappedMatches removes matches whose (task, workflow) has reached the
// consecutive-failure cap (settings.max_attempts), so a workflow that keeps
// failing is not re-dispatched forever. It is the internal backstop that does
// not depend on source-side labels — it stops runaway loops even for workflows
// with no on_fail. When a match is capped, the workflow's on_fail hook is
// applied best-effort (so an issue parks to needs-attention and leaves the poll
// set when the workflow defines one). Fail-open: on a count error the match is
// kept (the cap is a safety net, not a correctness guard).
func (d *Dispatcher) dropCappedMatches(ctx context.Context, task model.InternalTask, matches []router.Match) []router.Match {
	limit := d.cfg.Settings.MaxAttempts
	if limit <= 0 {
		return matches
	}
	out := make([]router.Match, 0, len(matches))
	for _, m := range matches {
		n, err := d.db.CountConsecutiveFailedInstances(ctx, task.ID, m.Route.ID)
		if err != nil {
			aplog.Error("failure-cap check task %s workflow %s: %v — dispatching anyway", task.ID, m.Route.ID, err)
			out = append(out, m)
			continue
		}
		if n >= limit {
			aplog.Warn("task %s: workflow %s hit failure cap (%d/%d consecutive failures), not re-dispatching", task.ID, m.Route.ID, n, limit)
			d.parkCappedTask(ctx, task, m.Route.ID)
			continue
		}
		out = append(out, m)
	}
	return out
}

// parkCappedTask applies a capped workflow's on_fail hook to the task's source
// bindings (set_state / add_labels) using the same machinery the engine uses, so
// the item parks (e.g. needs-attention) and stops being re-polled. A no-op when
// the workflow has no on_fail (e.g. the hatch-* escape hatches) — the cap still
// stops re-dispatch, the item just keeps being polled harmlessly.
func (d *Dispatcher) parkCappedTask(ctx context.Context, task model.InternalTask, workflowID string) {
	var onFail *config.OnComplete
	for i := range d.cfg.Workflows {
		if d.cfg.Workflows[i].ID == workflowID {
			onFail = d.cfg.Workflows[i].OnFail
			break
		}
	}
	if onFail == nil {
		return
	}
	bindings, err := d.db.ListBindingsByTask(ctx, task.ID)
	if err != nil {
		aplog.Error("park capped task %s: list bindings: %v", task.ID, err)
		return
	}
	if err := (&wfSideEffects{d: d}).ApplyHook(ctx, task, bindings, *onFail); err != nil {
		aplog.Error("park capped task %s: apply on_fail: %v", task.ID, err)
	}
}

// dispatch acknowledges, runs, and writes the result for a single task.
func (d *Dispatcher) dispatch(ctx context.Context, cell model.SourceItem, adapter source.Adapter, task model.InternalTask, match router.Match) model.RunResult {
	// Workflow mode is the only dispatch path: every matched task runs
	// through the workflow engine (instances + step runs + memory). A plain
	// route is synthesized into a single-step workflow by dispatchWorkflow.
	return d.dispatchWorkflow(ctx, cell, task, match)
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
			"edit": "allow",
			"bash": "allow",
			"read": "allow",
			"glob": "allow",
			"grep": "allow",
			"task": "allow",
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

// runnerConfigWithMCPs returns the runner's config map augmented with the
// resolved MCP servers (runner-scope defaults overlaid by agent-scope overrides,
// keyed by name) under the "mcps" key, which CliRunner.Configure consumes. The
// base map is never mutated — a shallow copy is returned only when there are
// servers to inject, so MCP-less runners keep their original config untouched.
func runnerConfigWithMCPs(base map[string]any, runnerMCPs, agentMCPs []model.MCPServer) map[string]any {
	merged := model.MergeMCPServers(runnerMCPs, agentMCPs)
	if len(merged) == 0 {
		return base
	}
	out := make(map[string]any, len(base)+1)
	for k, v := range base {
		out[k] = v
	}
	out["mcps"] = merged
	return out
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
