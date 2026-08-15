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
	"net/url"
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
	"github.com/orlandoburli/apiary/internal/memory"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/plugin"
	queuepkg "github.com/orlandoburli/apiary/internal/queue"
	"github.com/orlandoburli/apiary/internal/router"
	runnerimpl "github.com/orlandoburli/apiary/internal/runner"
	"github.com/orlandoburli/apiary/internal/source"
	"github.com/orlandoburli/apiary/internal/source/pluginsource"
	"github.com/orlandoburli/apiary/internal/version"
	workerpkg "github.com/orlandoburli/apiary/internal/worker"
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

	// memStore is the persistent agent memory store (settings.memory). nil when
	// memory is disabled; memDir is its root, exported to agent subprocesses as
	// APIARY_MEMORY_DIR.
	memStore *memory.Store
	memDir   string

	// binder records each polled SourceItem as an InternalTask + SourceBinding.
	// nil when no DB is configured (tests / dry-run without persistence).
	binder source.SourceBinder

	sem        chan struct{}            // poll concurrency (size 1)
	agentSem   map[string]chan struct{} // per-agent dispatch concurrency
	// bg tracks every goroutine the dispatcher spawns to carry a dispatch
	// forward off the poll loop (fan-out runs, PR-event runs, parked
	// approval/wait advances, resumes). Waiting on it gives callers — chiefly
	// tests, which own the DB's lifetime — a deterministic point at which no
	// dispatch work is still touching the store.
	bg         sync.WaitGroup
	active     atomic.Int32
	inFlight   sync.Map
	activeRuns sync.Map
	runCancel  sync.Map
	// instanceCancel holds the same cancel funcs as runCancel, keyed by workflow
	// instance id instead of cell id. One cell can carry two live instances (a
	// resumed run and a re-dispatched one, issue #422), and the cell-keyed map
	// only remembers whichever started last — so cancelling by cell cannot target
	// one of them. `apiary instances <id> --cancel` uses this map to stop exactly
	// the run the operator named.
	instanceCancel sync.Map
	// waitAdvancing guards a parked wait_for instance while its CI re-check (and any
	// follow-on advance) is in flight, so overlapping poll cycles don't double-check
	// or double-advance the same instance. Mirrors inFlight for polled cells.
	waitAdvancing sync.Map
	// approvalAdvancing is the approval-path twin of waitAdvancing: it guards a parked
	// approval instance while its cheap re-evaluation (and any follow-on resume/abort
	// advance) is in flight, so overlapping poll cycles don't double-advance it.
	approvalAdvancing sync.Map
	// autoResuming holds the (task, workflow) pairs whose interrupted instance is
	// being replayed by the startup auto-resume pass (resume: auto). It blocks a
	// concurrent poll from dispatching a fresh step-1 instance for the same pair
	// before the resume descendant exists in the DB (issue #376).
	autoResuming sync.Map
	// dropNotified remembers, per task id, the reason signature of the last
	// "every match was dropped" report, so the INFO line is emitted when a task
	// first goes fully-dropped (and again whenever the reason changes) instead of
	// once per poll interval for as long as a workflow runs. Cleared as soon as
	// the task dispatches something again.
	dropNotified sync.Map
	// staleInFlightWarned holds the cell ids already reported as holding a leaked
	// in-flight marker, so the warning is emitted once per cell rather than once
	// per poll interval for as long as the daemon runs.
	staleInFlightWarned sync.Map

	stats  map[string]*sourceStat
	statMu sync.RWMutex

	mu     sync.Mutex
	runSeq int

	// workflow engine — the dispatch path. Built once and long-lived so
	// instances parked at approval steps survive across dispatch cycles.
	engine     *workflow.Engine
	engineOnce sync.Once

	// eventExporters are isolated out-of-process plugin clients. Event storage
	// always completes first; exporter failures are reported but never returned
	// into dispatcher control flow.
	eventExporters []*plugin.Client

	// Durable dispatch queue and optional embedded local protocol-1 worker.
	dispatchQueue queuepkg.Store
	// warnedUnsatisfiable holds the ids of queued jobs already reported as
	// unleasable, so the periodic watchdog warns once per job instead of once per
	// tick. An id is forgotten as soon as the job becomes satisfiable again.
	warnedUnsatisfiable sync.Map
	// warnedStalled holds the ids of queued jobs already reported as satisfiable
	// but unleased, so the stall warning is emitted once per job.
	warnedStalled  sync.Map
	localWorker    *workerpkg.Runtime
	queueWorker    queuepkg.Worker
	queueProjectID string
	queueWorkerID  string
}

// runnerCandidate is one rung in an agent's rate-limit failover chain: a
// configured runner adapter, its adapter type (the rate-limit pause key), and
// the model to run it with.
type runnerCandidate struct {
	adapter    runnerimpl.Runner
	runnerType string
	model      string
}

// pluginExportStore preserves *db.Client's complete workflow Store surface via
// embedding while intercepting execution events for configured exporters.
type pluginExportStore struct {
	*db.Client
	dispatcher *Dispatcher
}

func (s pluginExportStore) RecordExecutionEvent(ctx context.Context, event *db.ExecutionEvent) error {
	return s.dispatcher.persistAndExportExecutionEvent(ctx, event)
}

// runnerPausedUntil returns the time a runner type's provider rate limit resets,
// or the zero time if it is not paused.
func (d *Dispatcher) runnerPausedUntil(runnerType string) time.Time {
	d.rateLimitMu.Lock()
	defer d.rateLimitMu.Unlock()
	return d.rateLimitPaused[runnerType]
}

// pauseRunner records that a runner type was paused (rate-limited, credit-
// exhausted, or aborted) until `until`. A zero `until` defaults based on the
// failure kind: rate-limit → 5m, credit-exhausted → 24h, aborted → 0. Only
// extends an existing pause, never shortens it.
func (d *Dispatcher) pauseRunner(runnerType string, until time.Time) {
	_ = d.pauseRunnerWithKind(runnerType, until, model.FailureNone)
}

// pauseRunnerWithKind records a pause with failure-kind-aware default duration.
// Returns the effective pause until time.
func (d *Dispatcher) pauseRunnerWithKind(runnerType string, until time.Time, kind model.FailureKind) time.Time {
	if until.IsZero() {
		switch kind {
		case model.FailureCreditExhausted:
			cooldown := 24 * time.Hour
			if d.cfg != nil {
				cooldown = d.cfg.Settings.CreditExhaustedCooldownDuration()
			}
			until = time.Now().Add(cooldown)
		case model.FailureAborted:
			until = time.Time{} // no pause for transient aborts
		default:
			until = time.Now().Add(5 * time.Minute)
		}
	}
	if until.IsZero() {
		return until
	}
	d.rateLimitMu.Lock()
	defer d.rateLimitMu.Unlock()
	if d.rateLimitPaused == nil {
		d.rateLimitPaused = make(map[string]time.Time)
	}
	if cur, ok := d.rateLimitPaused[runnerType]; !ok || until.After(cur) {
		d.rateLimitPaused[runnerType] = until
	}
	return until
}

// New builds and connects a Dispatcher from the given config.
// Pass nil for db and logger to skip state persistence and logging to SQLite.
// profileName selects a named runner profile (from cfg.Profiles) that overrides
// per-agent runner/model/fallbacks settings. An empty string means base config.
func New(ctx context.Context, cfg *config.Config, configFile string, dbClient *db.Client, logger *logging.Logger, profileName ...string) (*Dispatcher, error) {
	// Privilege ceiling: agent CLIs inherit the daemon's uid, so running as root
	// means a prompt-injected agent executes as root. Warn by default (a hard
	// refusal would break existing root service installs on upgrade); operators
	// can enforce it with settings.refuse_root.
	if os.Geteuid() == 0 {
		if cfg.Settings.RefuseRoot {
			return nil, fmt.Errorf("refusing to start as root (euid 0) because settings.refuse_root is set: agent CLIs would inherit root privileges — run the daemon as a dedicated non-root user")
		}
		aplog.Warn("running as root (euid 0): agent CLIs inherit root privileges, so a prompt injection would execute as root — run as a dedicated non-root user, or set settings.refuse_root: true to make this an error")
	}

	r, err := router.New(cfg)
	if err != nil {
		return nil, err
	}

	d := &Dispatcher{
		cfg:             cfg,
		configFile:      configFile,
		startedAt:       time.Now(),
		router:          r,
		sources:         make(map[string]source.Adapter),
		runners:         make(map[string]runnerimpl.Runner),
		agentRunner:     make(map[string]string),
		agentFallbacks:  make(map[string][]runnerCandidate),
		rateLimitPaused: make(map[string]time.Time),
		db:              dbClient,
		logger:          logger,
		sem:             make(chan struct{}, 1), // poll: one at a time
		agentSem:        make(map[string]chan struct{}),
		stats:           make(map[string]*sourceStat),
	}

	registry, pluginErrs := plugin.DiscoverConfigured(cfg.PluginDirs, configFile, version.Version)
	pluginErrs = append(pluginErrs, plugin.ValidateConfigured(registry, cfg.Plugins)...)
	if len(pluginErrs) > 0 {
		return nil, fmt.Errorf("plugins: %w", errors.Join(pluginErrs...))
	}
	d.eventExporters, pluginErrs = plugin.EnabledClients(registry, cfg.Plugins, plugin.CapabilityEventExporter)
	if len(pluginErrs) > 0 {
		return nil, fmt.Errorf("plugins: %w", errors.Join(pluginErrs...))
	}
	// Source-capable plugin clients, keyed by plugin id, resolved by `type:
	// plugin` sources (pluginsource bridge) below.
	sourceClients, pluginErrs := plugin.EnabledClients(registry, cfg.Plugins, plugin.CapabilitySource)
	if len(pluginErrs) > 0 {
		return nil, fmt.Errorf("plugins: %w", errors.Join(pluginErrs...))
	}
	sourcePlugins := make(map[string]*plugin.Client, len(sourceClients))
	for _, client := range sourceClients {
		sourcePlugins[client.ID()] = client
	}
	if dbClient != nil {
		dbClient.SetEventSensitiveFields(cfg.Settings.Events.SensitiveFields)
	}

	// The binder persists InternalTasks + SourceBindings; it needs the DB.
	if dbClient != nil {
		d.binder = source.NewSourceBinder(dbClient)
	}

	// Persistent agent memory (settings.memory). The root defaults to
	// <data-dir>/memory, beside apiary.db.
	if cfg.Settings.Memory.Enabled {
		root := cfg.Settings.Memory.Path
		if root == "" {
			root = filepath.Join(config.DataDir(configFile), "memory")
		}
		store, err := memory.Open(root)
		if err != nil {
			return nil, fmt.Errorf("memory store: %w", err)
		}
		store.MaxEntryBytes = cfg.Settings.Memory.MaxEntryBytes
		d.memStore = store
		d.memDir = root
		aplog.Info("agent memory enabled at %s", root)
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

		// Plugin-bridged sources resolve their plugin client at Connect; hand
		// them the enabled source-capable clients before connecting.
		if pb, ok := adapter.(interface {
			BindPluginLookup(func(pluginID string) (pluginsource.Invoker, bool))
		}); ok {
			pb.BindPluginLookup(func(pluginID string) (pluginsource.Invoker, bool) {
				client, ok := sourcePlugins[pluginID]
				return client, ok
			})
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

	// Apply the active profile overlay (if any) before building runners.
	// Profiles override per-agent runner/model/fallbacks/fallback_strategy from
	// the named entry in cfg.Profiles, selected via --profile=<name>.
	var activeProfile string
	if len(profileName) > 0 {
		activeProfile = profileName[0]
	}
	if activeProfile != "" {
		profile, ok := cfg.Profiles[activeProfile]
		if !ok {
			aplog.Warn("profile %q not found — continuing with base config", activeProfile)
		} else {
			for i := range cfg.Agents {
				ac := &cfg.Agents[i]
				overrides, ok := profile[ac.ID]
				if !ok {
					continue
				}
				if overrides.Runner != "" {
					ac.Runner = overrides.Runner
				}
				if overrides.Model != "" {
					ac.Model = overrides.Model
				}
				if overrides.Fallbacks != nil {
					ac.Fallbacks = overrides.Fallbacks
				}
				if overrides.FallbackStrategy != "" {
					ac.FallbackStrategy = overrides.FallbackStrategy
				}
			}
			aplog.Info("active profile: %s (%d agent overrides)", activeProfile, len(profile))
		}
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

		if err := ra.Configure(injectRunnerSecurity(runnerConfigWithMCPs(rc.Config, rc.MCPs, ac.MCPs), rc.Sandbox, rc.EnvPassthrough)); err != nil {
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
		// Agents without explicit fallbacks inherit settings.default_fallbacks.
		fallbacks := ac.Fallbacks
		if len(fallbacks) == 0 {
			fallbacks = cfg.Settings.DefaultFallbacks
		}
		for _, fb := range fallbacks {
			frc, ok := runnerMap[fb.Runner]
			if !ok {
				return nil, fmt.Errorf("agent %q: fallback runner %q not found", ac.ID, fb.Runner)
			}
			fAdapterName := frc.AdapterName()
			fra, ok := runnerimpl.New(fAdapterName)
			if !ok {
				return nil, fmt.Errorf("agent %q: fallback runner type %q not found", ac.ID, fAdapterName)
			}
			if err := fra.Configure(injectRunnerSecurity(runnerConfigWithMCPs(frc.Config, frc.MCPs, ac.MCPs), frc.Sandbox, frc.EnvPassthrough)); err != nil {
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
		if len(fallbacks) > 0 && len(ac.Fallbacks) == 0 {
			aplog.Info("agent %s: using default_fallbacks (%d entries)", ac.ID, len(fallbacks))
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

	// Must run after the agent loop above: the embedded worker advertises one
	// runner:<adapter> capability per entry in d.agentRunner, and enqueued jobs
	// require them. Configuring the queue before agents are loaded registers a
	// worker with no runner capabilities, so every job stays queued forever.
	if err := d.configureDispatchQueue(); err != nil {
		return nil, err
	}

	return d, nil
}

// Start launches one poll goroutine per source.
// Cancel ctx to initiate a graceful shutdown; then call wg.Wait().
func (d *Dispatcher) Start(ctx context.Context, wg *sync.WaitGroup) {
	// Enforce the shared git hooks directory on the agents' repo checkouts
	// (settings.git_hooks) before the first dispatch, so pre-push hooks gate
	// agent pushes from the very first run.
	installGitHooks(d.cfg.Settings.GitHooks)

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
		d.reconcileTerminalQueueJobs(ctx)
		d.warnUnsatisfiableQueueJobs(ctx)

		// Repair each non-terminal task's outstanding-workflow counter and settle
		// tasks stranded 'running' with no live instance. The instances just
		// interrupted above never decremented their task's counter, so without
		// this every mid-flight restart leaks +1 and the task can never reach
		// zero — it stays 'running' forever even after a later re-dispatch
		// completes (issue #198). Must run after the two reconciles above and
		// before the rehydration passes below.
		if recounted, settled, err := d.db.ReconcileOrphanTaskCounters(ctx); err != nil {
			aplog.Warn("reconcile orphan task counters: %v", err)
		} else if recounted > 0 || settled > 0 {
			aplog.Info("reconciled outstanding counter on %d task(s), settled %d stranded task(s)", recounted, settled)
		}
	}
	if d.db != nil {
		if retention := d.cfg.Settings.Events.RetentionDuration(); retention > 0 {
			if n, err := d.db.PruneExecutionEventsBefore(ctx, time.Now().Add(-retention)); err != nil {
				aplog.Warn("prune execution events: %v", err)
			} else if n > 0 {
				aplog.Info("pruned %d expired execution event(s)", n)
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				ticker := time.NewTicker(24 * time.Hour)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						_, _ = d.db.PruneExecutionEventsBefore(ctx, time.Now().Add(-retention))
					}
				}
			}()
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

	// Continue instances the reconcile above just marked interrupted when their
	// workflow declared `resume: auto`: replay the passed steps from cache and
	// re-run only what was in flight, instead of letting the first poll dispatch
	// a fresh run from step 1 and discard the completed work (issue #376). Must
	// run before the poll loops start so the dispatch guard is already claimed.
	d.resumeAutoInterrupted(ctx)

	// The embedded queue worker starts last, after every startup pass above.
	// It reclaims leases the dead process left behind and re-delivers those jobs
	// immediately, so starting it first (as it used to) raced the reconciles:
	// a job could be claimed before its instance was even marked interrupted,
	// and before auto-resume had claimed its dispatch guard — the redelivery
	// then ran the workflow from step 1 alongside the run being resumed, two
	// agents on one branch (issue #422). Nothing here dispatches work, so the
	// delay only postpones the first claim by the length of the reconcile pass.
	if d.localWorker != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := d.localWorker.Run(ctx); err != nil {
				aplog.Error("embedded worker: %v", err)
			}
		}()
	}

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

	// Prune task-memory notes whose task has been terminal longer than the
	// retention window (settings.memory.task_retention), at startup and then
	// every 6 hours. Global entries are never auto-pruned.
	if d.memStore != nil && d.db != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.pruneMemoryLoop(ctx)
		}()
	}

	// Back-fill cost_usd on finished cursor-cli executions from Cursor's
	// dashboard usage API (settings.cursor_cost), at startup and then every
	// sweep interval. The Cursor agent CLI streams token counts but no cost.
	if d.db != nil && d.cfg.Settings.CursorCost.Enabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.cursorCostLoop(ctx)
		}()
	}

	// Keep re-checking that every queued dispatch job can still be leased by some
	// worker. The startup check alone misses the case that actually bites: a job
	// enqueued later whose required pool/labels/capabilities nothing advertises,
	// which then sits queued forever in silence (#375).
	if d.dispatchQueue != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.queueWatchdogLoop(ctx)
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

// pruneMemoryLoop deletes task-memory note files whose task has been terminal
// (done/failed) longer than settings.memory.task_retention, at startup and then
// every 6 hours. A task is kept while any descendant is still non-terminal —
// spawned children inherit their ancestors' notes, so the chain must stay
// readable while work is in flight. Notes whose task ID is unknown (e.g. after
// a DB reset) fall back to the file's mtime against the same retention window.
func (d *Dispatcher) pruneMemoryLoop(ctx context.Context) {
	d.pruneTaskMemoryOnce(ctx)

	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.pruneTaskMemoryOnce(ctx)
		}
	}
}

// pruneTaskMemoryOnce runs one retention sweep over the task-memory note files
// and returns how many were deleted.
func (d *Dispatcher) pruneTaskMemoryOnce(ctx context.Context) int {
	retention := d.cfg.Settings.Memory.TaskRetentionDuration()
	notes, err := d.memStore.ListTaskNotes()
	if err != nil {
		aplog.Warn("prune task memory: %v", err)
		return 0
	}
	pruned := 0
	cutoff := time.Now().Add(-retention)
	for _, tn := range notes {
		task, err := d.db.InternalTasks().GetTask(ctx, tn.TaskID)
		if err != nil || task == nil {
			// Unknown task (DB reset / hand-created file): mtime fallback.
			if tn.ModTime.Before(cutoff) {
				if d.memStore.DeleteTaskNotes(tn.TaskID) == nil {
					pruned++
				}
			}
			continue
		}
		terminal := task.State == model.TaskStateDone || task.State == model.TaskStateFailed
		if !terminal || task.UpdatedAt.After(cutoff) {
			continue
		}
		if d.hasLiveDescendant(ctx, tn.TaskID, 0) {
			continue
		}
		if err := d.memStore.DeleteTaskNotes(tn.TaskID); err != nil {
			aplog.Warn("prune task memory %s: %v", tn.TaskID, err)
		} else {
			pruned++
		}
	}
	if pruned > 0 {
		aplog.Info("pruned %d task memory file(s) past the %s retention", pruned, retention)
	}
	return pruned
}

// hasLiveDescendant reports whether any spawned descendant of the task is still
// non-terminal. Depth-capped as cheap insurance against an accidental cycle.
func (d *Dispatcher) hasLiveDescendant(ctx context.Context, taskID string, depth int) bool {
	if depth > 32 {
		return false
	}
	children, err := d.db.InternalTasks().ListChildTasks(ctx, taskID)
	if err != nil {
		return true // unsure — keep the notes
	}
	for _, c := range children {
		if c.State != model.TaskStateDone && c.State != model.TaskStateFailed {
			return true
		}
		if d.hasLiveDescendant(ctx, c.ID, depth+1) {
			return true
		}
	}
	return false
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
			if _, loaded := d.inFlight.LoadOrStore(cell.ID, time.Now()); loaded {
				continue
			}
			task, persisted := d.bindItem(ctx, cell)
			d.recordRouteEvents(ctx, task)
			matches := d.router.RouteAll(task)
			if len(matches) == 0 {
				aplog.Debug("  %q: no route matched (source=%q labels=%v)", cell.Title, cell.SourceID, cell.Labels)
				d.inFlight.Delete(cell.ID)
				continue
			}
			d.fanOut(ctx, cell, adapter, task, persisted, matches, fanOutOpts{
				wg: &wg,
				onFail: func(id string) {
					mu.Lock()
					failedIDs = append(failedIDs, id)
					mu.Unlock()
				},
				ownsInFlight: true,
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
	if d.dispatchQueue != nil {
		resp.Queue.Enabled = true
		resp.Queue.Jobs = map[string]int{}
		if jobs, err := d.dispatchQueue.ListJobs(context.Background(), "", 1000); err == nil {
			for _, job := range jobs {
				resp.Queue.Jobs[string(job.State)]++
			}
		}
		if workers, err := d.dispatchQueue.ListWorkers(context.Background()); err == nil {
			for _, worker := range workers {
				resp.Queue.Workers = append(resp.Queue.Workers, QueueWorkerStatus{ID: worker.ID, Pool: worker.Pool, Ready: worker.Ready, Healthy: time.Since(worker.LastHeartbeat) <= d.cfg.Settings.Queue.WorkerTimeoutValue(), Draining: worker.Draining, Capacity: worker.Capacity, ActiveJobs: worker.ActiveJobs, LastHeartbeat: humanDuration(time.Since(worker.LastHeartbeat)) + " ago"})
			}
		}
	}

	return resp
}

// ErrUnknownCell is returned by ForceRestart when the id names no cell this
// daemon knows about. Restart is destructive — it cancels in-flight work — so an
// id that resolves to nothing must fail closed instead of running the restart
// side effects with an id from some other id space (#377).
var ErrUnknownCell = errors.New("unknown cell")

// RestartResult reports what a force-restart actually did. Restart used to return
// nothing but an error, so "the cleanup ran" and "a new run started" were
// indistinguishable to every caller — which is why the dashboard's R looked like a
// no-op even when it worked. Dispatched/Workflows say whether new work exists now;
// Overridden names the pre-dispatch guards an explicit restart deliberately
// ignored, so a bypass is never silent.
type RestartResult struct {
	CellID     string   `json:"cell_id"`
	Ref        string   `json:"ref,omitempty"` // human reference (CDT-123, #1953) if the item has one
	Dispatched int      `json:"dispatched"`
	Workflows  []string `json:"workflows,omitempty"`
	Overridden []string `json:"overridden,omitempty"`
}

// Label returns the reference to show a human: the item's own number when it has
// one, else the raw cell id. Reporting a bare cell id is close to useless on
// sources whose id is not what people see — a Jira restart would echo "10042" for
// what the user knows as CDT-123.
func (r RestartResult) Label() string {
	if r.Ref != "" && r.Ref != r.CellID {
		return fmt.Sprintf("%s (%s)", r.Ref, r.CellID)
	}
	return r.CellID
}

// ErrAmbiguousRef is returned when a human item reference matches items in more
// than one source. Restart is destructive, so a reference that could mean two
// different items must be disambiguated by the caller rather than guessed at.
var ErrAmbiguousRef = errors.New("ambiguous item reference")

// resolveCellRef maps whatever the caller passed to a cell id. A cell id is
// returned unchanged; anything else is looked up as a human item reference (Jira
// key, GitHub issue number, Dynatrace display id) against the source bindings.
//
// This exists because for several sources the cell id is not a thing any human
// ever sees: the Jira adapter binds on the opaque numeric issue id, so `apiary
// restart CDT-123` — the only form a user could reasonably type — failed as an
// unknown cell while the id that did work appeared nowhere in the UI.
// sourceID, when set, scopes the lookup to one source: it is how a caller
// resolves an ErrAmbiguousRef ("PSP-199 exists in two sources") rather than
// having to find the cell id by hand.
func (d *Dispatcher) resolveCellRef(ctx context.Context, ref, sourceID string) (cellID, number string, err error) {
	ref = strings.TrimSpace(ref)
	if ref == "" || d.db == nil {
		return ref, "", nil
	}

	// An exact cell id always wins: it is unambiguous, and a source whose ids and
	// numbers overlap (GitHub, where both derive from the issue number) must not
	// take the slower path.
	if b, err := d.db.SourceBindings().GetBindingBySourceItemID(ctx, ref); err == nil && b != nil {
		if sourceID == "" || b.SourceID == sourceID {
			return b.SourceItemID, b.SourceItemNumber, nil
		}
	}

	bindings, err := d.db.SourceBindings().ListBindingsBySourceItemNumber(ctx, ref)
	if err != nil {
		return ref, "", fmt.Errorf("restart %s: looking up item reference: %w", ref, err)
	}
	if sourceID != "" {
		scoped := bindings[:0]
		for _, b := range bindings {
			if b.SourceID == sourceID {
				scoped = append(scoped, b)
			}
		}
		bindings = scoped
	}
	if len(bindings) == 0 {
		return ref, "", nil // not a known reference; the caller decides
	}

	// Several bindings are fine when they are the same item (one item can be bound
	// more than once over its life); distinct items are not.
	distinct := map[string]model.SourceBinding{}
	for _, b := range bindings {
		distinct[b.SourceID+":"+b.SourceItemID] = b
	}
	if len(distinct) > 1 {
		var opts []string
		for _, b := range distinct {
			opts = append(opts, fmt.Sprintf("%s:%s", b.SourceID, b.SourceItemID))
		}
		sort.Strings(opts)
		return ref, "", fmt.Errorf("%w: %q matches %d items (%s) — name the source, or use the cell id directly",
			ErrAmbiguousRef, ref, len(distinct), strings.Join(opts, ", "))
	}

	b := bindings[0]
	aplog.Info("restart: resolved item reference %q to cell %s (source %s)", ref, b.SourceItemID, b.SourceID)
	return b.SourceItemID, b.SourceItemNumber, nil
}

// ForceRestart cancels a running dispatch for the given cell, removes it from
// tracking maps, marks the execution as interrupted in the DB, resets the source
// state, strips the cell's control labels — and then re-routes and dispatches the
// cell immediately rather than leaving it for the next poll.
//
// The immediate dispatch is the point: waiting for the poll tick meant a restart
// produced no observable effect for up to a full poll_interval, and on the queue
// path it usually produced none at all (the dispatch generation never moved, so
// every re-enqueue collided with the restarted round's own idempotency keys).
// Restart now bumps the generation and dispatches inline; the poll remains a
// fallback for anything this path cannot resolve (e.g. a source with no
// TaskPoller).
//
// An explicit restart also overrides the `once` and failure-cap guards: those
// describe what automatic re-polling may do on its own, and a task wedged behind
// either is exactly the task a human reaches for restart to unwedge. The
// in-flight guard is NOT overridden — restart never runs a workflow twice
// concurrently.
//
// The argument may be a cell id (the source item id) or the item's human
// reference (a Jira key like CDT-123, a GitHub issue number like #1953) — the
// latter is resolved through the source bindings. It may NOT be an internal task
// id; that, and anything else that resolves to nothing, returns ErrUnknownCell
// and touches nothing.
func (d *Dispatcher) ForceRestart(ctx context.Context, ref string) (RestartResult, error) {
	cellID, number, err := d.resolveCellRef(ctx, ref, "")
	if err != nil {
		return RestartResult{CellID: ref}, err
	}
	res := RestartResult{CellID: cellID, Ref: number}
	if err := d.assertKnownCell(ctx, cellID); err != nil {
		return res, err
	}

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
	// (e.g. "agent:engineer"). Needs the source's current labels (TaskPoller);
	// removal additionally needs LabelRemover. The fetched item is also what the
	// inline re-dispatch below routes on, so a source that can only be read still
	// yields a restart — it just cannot clear labels.
	var (
		restartCell    model.SourceItem
		restartAdapter source.Adapter
	)
	for _, sc := range d.cfg.Sources {
		adapter, ok := d.sources[sc.ID]
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
		// Never write to an item the adapter substituted for the one asked for:
		// stripping labels off an unrelated cell is exactly the mis-targeting
		// #377 reported. An empty id means the adapter does not report one.
		if cell.ID != "" && cell.ID != cellID {
			aplog.Error("force-restart %s: %s returned item %s — refusing to touch it", cellID, sc.ID, cell.ID)
			continue
		}
		labels := d.controlLabels(cell)
		if remover, ok := adapter.(source.LabelRemover); ok && len(labels) > 0 {
			if err := remover.RemoveLabels(ctx, cell, labels); err != nil {
				aplog.Error("force-restart %s: removing control labels %v: %v", cellID, labels, err)
			} else {
				aplog.Info("force-restart %s: removed control labels %v", cellID, labels)
			}
		}
		if restartAdapter == nil {
			// Route on the post-strip label set. Re-reading it from the source
			// would race the removal we just issued (forges apply label writes
			// asynchronously), and a stale read puts the lock label straight back
			// into routing — the cell would be dropped by the very exclusion the
			// strip exists to clear.
			cell.ID = cellID
			cell.Labels = withoutLabels(cell.Labels, labels)
			restartCell, restartAdapter = cell, adapter
		}
	}

	aplog.Info("force-restarted cell %s", cellID)

	// Dispatch now instead of deferring to the next poll tick.
	if restartAdapter != nil {
		res.Dispatched, res.Workflows, res.Overridden = d.redispatchCell(ctx, restartCell, restartAdapter)
	} else {
		aplog.Warn("force-restart %s: no source could fetch the item — falling back to the next poll for re-dispatch", cellID)
	}
	return res, nil
}

// withoutLabels returns labels minus remove, compared case-insensitively to match
// controlLabels' own comparison.
func withoutLabels(labels, remove []string) []string {
	if len(remove) == 0 {
		return labels
	}
	drop := make(map[string]bool, len(remove))
	for _, l := range remove {
		drop[strings.ToLower(l)] = true
	}
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		if !drop[strings.ToLower(l)] {
			out = append(out, l)
		}
	}
	return out
}

// redispatchCell re-routes a just-restarted cell and dispatches every workflow
// that matches, returning the count, the workflow ids, and the guards it
// overrode. It is the poll loop's per-cell body minus the guards an explicit
// restart is meant to defeat: `once` and the consecutive-failure cap are skipped
// (and reported), while the in-flight guard still applies so a restart can never
// double-run a live workflow.
//
// The dispatch generation is bumped before fan-out: the queue keys jobs on
// taskID:generation:routeID, so without a bump every re-enqueue would be
// swallowed as a duplicate of the round that was just interrupted.
func (d *Dispatcher) redispatchCell(ctx context.Context, cell model.SourceItem, adapter source.Adapter) (int, []string, []string) {
	if d.router == nil {
		// Reachable from the IPC path in harnesses that wire a dispatcher without
		// a router; the poll loop always has one. Cleanup already ran, so the
		// restart itself stands — there is just nothing to route with.
		return 0, nil, nil
	}
	if _, loaded := d.inFlight.LoadOrStore(cell.ID, time.Now()); loaded {
		aplog.Warn("force-restart %s: cell went in-flight again before re-dispatch — leaving it to the poll", cell.LogLabel())
		return 0, nil, nil
	}

	task, persisted := d.bindItem(ctx, cell)
	d.recordRouteEvents(ctx, task)
	matches := d.router.RouteAll(task)

	var overridden []string
	if persisted && d.db != nil {
		var dropped []droppedMatch
		matches, dropped = d.dropAutoResumingMatches(task.ID, matches)
		var batch []droppedMatch
		matches, batch = d.dropActiveMatches(ctx, task.ID, matches)
		dropped = append(dropped, batch...)

		// Report what a normal poll would additionally have dropped, then keep
		// those matches anyway. Restart is the documented escape hatch for both.
		if _, once := d.dropOnceMatches(ctx, task.ID, matches); len(once) > 0 {
			for _, m := range once {
				aplog.Info("force-restart %s: overriding `once` guard for workflow %s (%s)", cell.LogLabel(), m.WorkflowID, m.Detail)
				overridden = append(overridden, m.WorkflowID+" (once)")
			}
		}
		if _, capped := d.dropCappedMatches(ctx, task, matches); len(capped) > 0 {
			for _, m := range capped {
				aplog.Info("force-restart %s: overriding failure cap for workflow %s (%s)", cell.LogLabel(), m.WorkflowID, m.Detail)
				overridden = append(overridden, m.WorkflowID+" (failure cap)")
			}
		}
		for _, m := range dropped {
			aplog.Info("force-restart %s: workflow %s not re-dispatched (%s)", cell.LogLabel(), m.WorkflowID, m.Reason)
		}
	}

	if len(matches) == 0 {
		aplog.Warn("force-restart %s: nothing to dispatch — no workflow matches the cell in its current state", cell.LogLabel())
		d.inFlight.Delete(cell.ID)
		return 0, nil, overridden
	}

	// Cancel the interrupted round's queue jobs. Cancelling the run context only
	// reaches an inline dispatch; a queued or leased job survives it and would be
	// claimed later, running the round restart just tore down alongside the fresh
	// one. Queued jobs go terminal immediately, leased ones are flagged so their
	// worker stops at the next heartbeat.
	if persisted && d.dispatchQueue != nil {
		for _, m := range matches {
			if n, err := d.dispatchQueue.RequestCancelFor(ctx, task.ID, m.Route.ID); err != nil {
				aplog.Error("force-restart %s: cancel queued jobs for workflow %s: %v", cell.LogLabel(), m.Route.ID, err)
			} else if n > 0 {
				aplog.Info("force-restart %s: cancelled %d queued/leased job(s) for workflow %s", cell.LogLabel(), n, m.Route.ID)
			}
		}
	}

	if persisted && d.db != nil {
		if gen, err := d.db.InternalTasks().BumpGeneration(ctx, task.ID); err != nil {
			aplog.Error("force-restart %s: bump dispatch generation: %v — re-dispatch may be deduplicated away", cell.LogLabel(), err)
		} else {
			aplog.Debug("force-restart %s: dispatch generation now %d", cell.LogLabel(), gen)
		}
	}

	ids := make([]string, 0, len(matches))
	for _, m := range matches {
		ids = append(ids, m.Route.ID)
	}
	d.dropNotified.Delete(task.ID)
	aplog.Info("force-restart %s: dispatching %d workflow(s): %v", cell.LogLabel(), len(ids), ids)
	d.fanOut(ctx, cell, adapter, task, persisted, matches, fanOutOpts{ownsInFlight: true})
	return len(ids), ids, overridden
}

// assertKnownCell fails closed (ErrUnknownCell) unless cellID names a cell this
// daemon actually knows: an in-flight run, a source binding, a workflow instance,
// or a legacy execution row. Restart is destructive, and every step of it — the DB
// updates, the source SetState, the label stripping — used to run unconditionally
// on whatever raw id arrived, so an id from another id space (an internal task id,
// or an item id that only exists in a different source) was applied blindly while
// the CLI reported success. See #377.
//
// When the id is an internal task id, the error names the cell to use instead;
// accepting the task id as an alias is deliberately left out of scope.
func (d *Dispatcher) assertKnownCell(ctx context.Context, cellID string) error {
	if cellID == "" {
		return fmt.Errorf("%w: (empty id)", ErrUnknownCell)
	}
	// In-flight work is authoritative even before anything is persisted.
	if _, ok := d.inFlight.Load(cellID); ok {
		return nil
	}
	if _, ok := d.runCancel.Load(cellID); ok {
		return nil
	}
	if d.db == nil {
		// No store to validate against (unit harnesses); tracking maps are all
		// there is, and the daemon always has a DB.
		return nil
	}

	if b, err := d.db.SourceBindings().GetBindingBySourceItemID(ctx, cellID); err != nil {
		return fmt.Errorf("restart %s: looking up binding: %w", cellID, err)
	} else if b != nil {
		return nil
	}
	if insts, err := d.db.ListWorkflowInstancesByCell(ctx, cellID); err != nil {
		return fmt.Errorf("restart %s: listing workflow instances: %w", cellID, err)
	} else if len(insts) > 0 {
		return nil
	}
	if exec, err := d.db.GetLastExecution(ctx, cellID); err != nil {
		return fmt.Errorf("restart %s: looking up executions: %w", cellID, err)
	} else if exec != nil {
		return nil
	}

	// Nothing matched. If the caller passed an internal task id, point at the
	// reference that would have worked instead of a bare "unknown" — the item's
	// own number when it has one, since that is what the user can actually see.
	if t, err := d.db.InternalTasks().GetTask(ctx, cellID); err == nil && t != nil {
		if cell := d.cellIDForTask(ctx, t.ID); cell != "" && cell != cellID {
			suggest := cell
			if bs, err := d.db.ListBindingsByTask(ctx, t.ID); err == nil && len(bs) > 0 && bs[0].SourceItemNumber != "" {
				suggest = bs[0].SourceItemNumber
			}
			return fmt.Errorf("%w: %s is an internal task id, not a cell id — its item is %s (try: apiary restart %s)",
				ErrUnknownCell, cellID, cell, suggest)
		}
	}
	return fmt.Errorf("%w: %s", ErrUnknownCell, cellID)
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
	mux.HandleFunc("/events", d.handleExecutionEvents)
	mux.HandleFunc("/events/stream", d.handleExecutionEventStream)
	mux.HandleFunc("/approvals", d.handleApprovals)
	mux.HandleFunc("/approvals/", d.handleApprovalResponse)
	mux.HandleFunc("/restart/", func(w http.ResponseWriter, r *http.Request) {
		// Human references reach this route too (CDT-123, #1953), and '#' must be
		// percent-encoded by the caller or it would be read as a URL fragment and
		// never arrive. Decode it back before resolving.
		cellID := strings.TrimPrefix(r.URL.EscapedPath(), "/restart/")
		if decoded, err := url.PathUnescape(cellID); err == nil {
			cellID = decoded
		}
		if cellID == "" {
			http.Error(w, "missing cell id or item reference", http.StatusBadRequest)
			return
		}
		res, err := d.ForceRestart(ctx, cellID)
		if err != nil {
			status := http.StatusInternalServerError
			switch {
			case errors.Is(err, ErrUnknownCell):
				status = http.StatusNotFound
			case errors.Is(err, ErrAmbiguousRef):
				status = http.StatusConflict
			}
			http.Error(w, err.Error(), status)
			return
		}
		// Report what was dispatched, not just that the call succeeded: callers
		// need to distinguish "restarted and N workflows are running" from
		// "restarted and nothing matched".
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(res)
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
	mux.HandleFunc("/instances/compare", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		before, after := r.URL.Query().Get("before"), r.URL.Query().Get("after")
		if before == "" || after == "" {
			http.Error(w, "before and after instance ids are required", http.StatusBadRequest)
			return
		}
		comparison, err := d.CompareInstances(r.Context(), before, after)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if comparison == nil {
			http.Error(w, "instance not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(comparison)
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
		opts := ResumeOptions{FromStep: r.URL.Query().Get("from"), ConfigMode: r.URL.Query().Get("config")}
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
			preview, err := d.ResumePreview(r.Context(), id, opts)
			if err != nil {
				http.Error(w, err.Error(), resumeHTTPStatus(err))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(preview)
		case http.MethodPost:
			// Launch on the daemon-lifetime ctx so the run outlives the request.
			newID, err := d.StartResume(ctx, id, opts)
			if err != nil {
				http.Error(w, err.Error(), resumeHTTPStatus(err))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"queued": true, "instance_id": newID, "resumed_from": id})
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
	// POST /workflows/{id}/run — start one named workflow on demand, bypassing
	// every trigger and pre-dispatch guard. Registered on the subtree so the
	// exact-match /workflows listing above keeps its own handler.
	mux.HandleFunc("/workflows/", d.manualRunHandler(ctx))
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
			status := http.StatusInternalServerError
			if errors.Is(err, ErrInstanceNotFound) {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
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
	if err := d.startQueueProtocolServer(ctx, wg); err != nil {
		_ = srv.Close()
		return err
	}

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

	// The incremental watermark only advances after a poll that actually reached
	// the source. A failed poll that advanced it anyway made every item updated
	// inside that window invisible forever: the next poll asks for changes since a
	// moment it never observed, so a label hand-off performed during the outage is
	// never seen again and the task stalls until a restart's full rescan (#375).
	var lastPoll time.Time
	if err := d.poll(ctx, sc, adapter, lastPoll); err == nil {
		lastPoll = time.Now()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			started := time.Now()
			if err := d.poll(ctx, sc, adapter, lastPoll); err == nil {
				lastPoll = started
			} else {
				aplog.Warn("source %s: keeping incremental watermark at %s after a failed poll so updates in that window are not skipped", sc.ID, lastPoll.Format(time.RFC3339))
			}
		}
	}
}

// poll runs one source cycle: re-check parked work, fetch items changed since
// the watermark, route each one, and dispatch what survives the pre-dispatch
// guards. It returns the source's fetch error (nil on success) so the caller can
// decide whether the incremental watermark may advance.
func (d *Dispatcher) poll(ctx context.Context, sc config.SourceConfig, adapter source.Adapter, since time.Time) error {
	// Re-evaluate any workflows parked at approval steps against their live tasks
	// on each poll cycle (resume/abort/timeout) before fetching new work.
	d.checkApprovals(ctx)

	// Re-check any workflows parked at wait_for steps (e.g. waiting for CI) so they
	// advance, fail, or keep waiting based on live status.
	d.checkWaits(ctx)

	// Stop investigations whose alert resolved while they ran (opt-in).
	d.checkResolved(ctx, sc, adapter)

	aplog.Debug("polling source %s (since %s)", sc.ID, since.Format(time.RFC3339))
	cells, err := adapter.Poll(ctx, since)
	if err != nil {
		aplog.Error("source %s: poll error: %v", sc.ID, err)
		return err
	}
	aplog.Info("source %s: found %d cell(s)", sc.ID, len(cells))
	d.recordPoll(sc.ID, len(cells))

	sort.Slice(cells, func(i, j int) bool {
		return cells[i].CreatedAt.Before(cells[j].CreatedAt)
	})

	for _, cell := range cells {
		cell := cell
		if since, loaded := d.inFlight.LoadOrStore(cell.ID, time.Now()); loaded {
			// The originally-suspected cause of #375 was an in-flight marker that
			// was never released, which permanently poisoned the cell — invisibly,
			// because this is the only line that mentions it and it is DEBUG. The
			// marker is legitimately held only for as long as a dispatch takes to
			// start, so a cell still marked after inFlightStaleAfter is a defect,
			// not a busy cell: say so once, loudly, with the age.
			d.reportStaleInFlight(ctx, cell, since)
			aplog.Debug("cell %s: already in-flight, skipping", cell.LogLabel())
			continue
		}
		task, persisted := d.bindItem(ctx, cell)
		d.recordRouteEvents(ctx, task)
		matches, suppressedMatches := d.router.RouteAllWithSuppressed(task)
		// An exclusive trigger stops routing at itself, so the routes below it were
		// never considered. If a guard now removes that winner the task is left with
		// nothing, and the suppressed routes are not reconsidered — deliberately, as
		// falling through would duplicate the run the guard is protecting. Carry them
		// so the fully-dropped report can name what the exclusive claim cost.
		suppressed := suppressedRoutes{}
		if len(suppressedMatches) > 0 && len(matches) > 0 {
			suppressed.ExclusiveWorkflowID = matches[len(matches)-1].Route.ID
			for _, m := range suppressedMatches {
				suppressed.WorkflowIDs = append(suppressed.WorkflowIDs, m.Route.ID)
			}
		}
		// Source-agnostic in-flight guard: drop any matched workflow that already
		// has a non-terminal instance for this task, so a re-poll does not dispatch
		// a duplicate while it runs or waits at an approval step. Replaces the
		// label-based lock (state_lock / "in-progress"), which is source-specific.
		routed := len(matches)
		var dropped []droppedMatch
		if persisted && d.db != nil {
			var batch []droppedMatch
			matches, batch = d.dropAutoResumingMatches(task.ID, matches)
			dropped = append(dropped, batch...)
			matches, batch = d.dropActiveMatches(ctx, task.ID, matches)
			dropped = append(dropped, batch...)
			matches, batch = d.dropOnceMatches(ctx, task.ID, matches)
			dropped = append(dropped, batch...)
			matches, batch = d.dropCappedMatches(ctx, task, matches)
			dropped = append(dropped, batch...)
		}
		if len(matches) == 0 {
			if routed == 0 || len(dropped) == 0 {
				aplog.Debug("cell %s (%q): no matching route, skipping", cell.LogLabel(), cell.Title)
			} else {
				// Every route that matched was removed by a pre-dispatch guard:
				// nothing will run for this cell, and until #380 that was entirely
				// silent. Report it (once per distinct reason set) so a wedged task
				// is distinguishable from an idle one.
				d.reportFullyDropped(ctx, cell, task, dropped, suppressed)
			}
			d.inFlight.Delete(cell.ID)
			continue
		}
		d.dropNotified.Delete(task.ID)
		d.fanOut(ctx, cell, adapter, task, persisted, matches, fanOutOpts{ownsInFlight: true})
	}

	// PR events (trigger.on: pr_*) ride the same poll cadence, after item
	// dispatch. Capability-gated inside; a no-event config is a no-op.
	d.pollPREvents(ctx, sc, adapter)
	return nil
}

// fanOut dispatches every workflow matched for a polled cell. One InternalTask
// may match several triggers, so the cell fans out to N workflows. The task's
// outstanding-workflow counter is bumped by len(matches) before any workflow
// starts (so a completion hook can tell when all have finished); then one
// dispatch goroutine is launched per match, each admitted through its agent's
// semaphore and tracked as an active run. The cell's in-flight marker is cleared
// only after every dispatch finishes. When wg is non-nil each dispatch joins it
// (so RunOnce can wait); onFail, if set, is called with the cell ID per failure.
// goBackground runs fn on its own goroutine, tracked by d.bg so WaitBackground
// can tell when all in-flight dispatch work has finished. Every dispatcher
// goroutine that outlives the call that started it — and touches the DB — must
// go through here; otherwise it can still be running when the store closes.
func (d *Dispatcher) goBackground(fn func()) {
	d.bg.Add(1)
	go func() {
		defer d.bg.Done()
		fn()
	}()
}

// WaitBackground blocks until every goroutine started through goBackground has
// returned, or timeout elapses; it reports whether they all finished. Callers
// that own the dispatcher's DB (tests, one-shot runs) use it to close the store
// only once no dispatch is still writing to it.
func (d *Dispatcher) WaitBackground(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		d.bg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// fanOutOpts carries the knobs that differ between fan-out callers. It exists so
// the manual-run path can join the same dispatch machinery — queue, semaphores,
// activeRuns, outstanding accounting — while differing on the two things a manual
// run must not share with a poll: the in-flight marker it does not own, and the
// queue idempotency key it must not collide with.
type fanOutOpts struct {
	// wg, when non-nil, is signalled per dispatch AND forces inline dispatch:
	// callers that wait on the runs cannot have them handed to a queue worker.
	wg *sync.WaitGroup
	// onFail is called with the cell id for every dispatch that returns
	// Success == false.
	onFail func(cellID string)
	// idemSuffix widens the queue idempotency key beyond taskID:generation:routeID.
	// Empty for poll and restart, which rely on that key to de-duplicate overlapping
	// rounds; set per run by a manual dispatch, whose whole point is that a second
	// identical run is a second run and not a duplicate.
	idemSuffix string
	// ownsInFlight reports whether this fan-out stored d.inFlight[cell.ID] and may
	// therefore clear it when the dispatches finish. A manual run never stores it,
	// and clearing another path's marker would release a concurrent poll's guard
	// early — the next tick would then dispatch the cell a second time.
	ownsInFlight bool
}

func (d *Dispatcher) fanOut(ctx context.Context, cell model.SourceItem, adapter source.Adapter, task model.InternalTask, persisted bool, matches []router.Match, opts fanOutOpts) {
	wg, onFail := opts.wg, opts.onFail
	releaseInFlight := func() {
		if opts.ownsInFlight {
			d.inFlight.Delete(cell.ID)
		}
	}
	if len(matches) == 0 {
		releaseInFlight()
		return
	}
	if persisted && d.dispatchQueue != nil && wg == nil {
		d.enqueueFanOut(ctx, cell, task, matches, opts.idemSuffix)
		releaseInFlight()
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

		d.bg.Add(1)
		go func(runID string, match router.Match, agentCh chan struct{}) {
			defer d.bg.Done()
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
				cell.LogLabel(), cell.Title, match.Route.ID, match.Route.Agent)

			result := d.dispatch(ctx, cell, adapter, task, match)
			if !result.Success && onFail != nil {
				onFail(cell.ID)
			}
		}(runID, match, agentCh)
	}

	// Release the in-flight marker once all of this cell's dispatches finish, so
	// the cell can be re-polled (and re-routed against its refreshed task).
	d.goBackground(func() {
		inner.Wait()
		releaseInFlight()
	})
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
	existing, _ := d.db.SourceBindings().GetBindingBySourceItem(ctx, cell.SourceID, cell.ID)
	task, err := d.binder.Bind(ctx, cell)
	if err != nil {
		aplog.Error("bind source item %s (%q): %v", cell.LogLabel(), cell.Title, err)
		return transientTask(cell), false
	}
	aplog.Debug("bound source item %s → task %s [%s]", cell.LogLabel(), task.ID, task.State)
	d.recordExecutionEvent(ctx, db.ExecutionEvent{Type: "task.discovered", TaskID: task.ID, Metadata: map[string]any{"source_id": cell.SourceID, "source_item_id": cell.ID, "title": cell.Title}})
	eventType := "task.bound"
	if existing != nil {
		eventType = "task.refreshed"
	}
	d.recordExecutionEvent(ctx, db.ExecutionEvent{Type: eventType, TaskID: task.ID, Metadata: map[string]any{"source_id": cell.SourceID, "source_item_id": cell.ID}})
	return task, true
}

func (d *Dispatcher) recordExecutionEvent(ctx context.Context, event db.ExecutionEvent) {
	if d.db == nil {
		return
	}
	if err := d.persistAndExportExecutionEvent(ctx, &event); err != nil {
		aplog.Error("execution event %s: persist: %v", event.Type, err)
	}
}

func (d *Dispatcher) persistAndExportExecutionEvent(ctx context.Context, event *db.ExecutionEvent) error {
	if d.db == nil {
		return nil
	}
	if err := d.db.RecordExecutionEvent(ctx, event); err != nil {
		return err
	}
	for _, exporter := range d.eventExporters {
		if err := exporter.Invoke(ctx, plugin.CapabilityEventExporter, "export", *event, nil); err != nil {
			aplog.Error("plugin %s: export execution event %s: %v", exporter.ID(), event.Type, err)
		}
	}
	return nil
}

func (d *Dispatcher) recordRouteEvents(ctx context.Context, task model.InternalTask) {
	for _, trace := range d.router.ExplainTask(task) {
		eventType := "route.rejected"
		if trace.Selected {
			eventType = "route.selected"
		}
		d.recordExecutionEvent(ctx, db.ExecutionEvent{Type: eventType, TaskID: task.ID, WorkflowID: trace.RouteID,
			Metadata: map[string]any{"reason": trace.Reason, "priority": trace.Priority, "agent": trace.Agent, "worker": trace.Worker}})
	}
}

// droppedMatch records one workflow a pre-dispatch guard removed, and why. The
// guards used to drop silently (a DEBUG line at most), so a task whose every
// match was dropped looked exactly like an idle one: the poll reported the cell
// and nothing else was ever said about it (issue #380). Collecting the reason
// lets poll report the wedge at INFO and persist it as an execution event.
type droppedMatch struct {
	WorkflowID string
	// Reason is a short, stable token — "active instance", "once", "capped",
	// "auto-resuming", "guard error" — safe to log and to store in an event.
	Reason string
	// Detail is optional human context appended to the log line.
	Detail string
}

// String renders "workflow (reason: detail)" for a log line.
func (d droppedMatch) String() string {
	if d.Detail == "" {
		return fmt.Sprintf("%s (%s)", d.WorkflowID, d.Reason)
	}
	return fmt.Sprintf("%s (%s: %s)", d.WorkflowID, d.Reason, d.Detail)
}

// dropActiveMatches removes matches whose workflow already has a non-terminal
// instance for this task (running or approval_waiting). It is the source-agnostic
// in-flight guard: the in-memory inFlight marker is released when a workflow parks
// at an approval step, so without this a later poll would dispatch a duplicate
// while the instance is still waiting. Keyed on (task, workflow) so a completed
// earlier workflow (e.g. triage) does not block the one a hand-off routes to.
// Terminal states — including 'interrupted' — never block: an instance stranded
// by a restart stays eligible for re-dispatch or manual resume (issue #380).
// Fail-closed: on a query error the match is dropped (skip this poll, retry next)
// rather than risk a duplicate dispatch.
//
// Returns the surviving matches and, for each removed one, why it was removed.
func (d *Dispatcher) dropActiveMatches(ctx context.Context, taskID string, matches []router.Match) ([]router.Match, []droppedMatch) {
	out := make([]router.Match, 0, len(matches))
	var dropped []droppedMatch
	for _, m := range matches {
		active, err := d.db.HasActiveInstanceForRoute(ctx, taskID, m.Route.ID)
		if err != nil {
			aplog.Error("in-flight check task %s workflow %s: %v — skipping this poll", taskID, m.Route.ID, err)
			dropped = append(dropped, droppedMatch{WorkflowID: m.Route.ID, Reason: "guard error", Detail: err.Error()})
			continue
		}
		if active {
			aplog.Debug("task %s: workflow %s already active (running/approval_waiting), skipping re-dispatch", taskID, m.Route.ID)
			dropped = append(dropped, droppedMatch{WorkflowID: m.Route.ID, Reason: "active instance"})
			continue
		}
		out = append(out, m)
	}
	return out, dropped
}

// inFlightStaleAfter is how long a polled cell may stay marked in-flight before
// the marker is treated as leaked rather than busy. The marker guards only the
// window between routing a cell and handing its dispatches off (synchronously on
// the queue path; until the last dispatch goroutine starts otherwise), so any
// cell still marked after this long is a bug, not a long agent run.
const inFlightStaleAfter = 15 * time.Minute

// reportStaleInFlight warns once per cell when its in-flight marker has been held
// implausibly long. #375 was first diagnosed as exactly this leak, and the only
// evidence would have been a DEBUG line that says nothing about how long the
// marker has been held — so a recurrence was undiagnosable from an INFO log.
func (d *Dispatcher) reportStaleInFlight(ctx context.Context, cell model.SourceItem, stored any) {
	since, ok := stored.(time.Time)
	if !ok || time.Since(since) < inFlightStaleAfter {
		return
	}
	if _, warned := d.staleInFlightWarned.LoadOrStore(cell.ID, struct{}{}); warned {
		return
	}
	aplog.Warn("cell %s: in-flight marker held since %s (%s) — every poll has skipped this cell since then and no workflow can be dispatched for it until the daemon restarts; please report this on issue #375 with the log around that timestamp",
		cell.LogLabel(), since.Format(time.RFC3339), time.Since(since).Truncate(time.Second))
	d.recordExecutionEvent(ctx, db.ExecutionEvent{Type: "dispatch.dropped", WorkflowID: "",
		Metadata: map[string]any{"reason": "stale in-flight marker", "detail": time.Since(since).Truncate(time.Second).String(), "source_item_id": cell.ID}})
}

// suppressedRoutes names the lower-priority workflows an exclusive trigger stopped
// the router from considering, and the exclusive workflow that did it. It is empty
// unless an exclusive route matched. The dispatcher never dispatches these: see
// router.RouteAllWithSuppressed for why re-routing to them would duplicate work.
// It carries them only so a fully-dropped report can say what the exclusive claim
// suppressed — otherwise a task whose exclusive winner is dropped by a guard looks
// like a task with no viable workflow at all.
type suppressedRoutes struct {
	ExclusiveWorkflowID string
	WorkflowIDs         []string
}

// dropSignature is the stable identity of a "fully dropped" outcome: the set of
// (workflow, reason) pairs plus any suppressed routes, order-independent. Two
// consecutive polls that drop the same workflows for the same reasons share a
// signature and are reported once; a config change that adds or removes a
// suppressed fallback changes it, so the new picture is reported.
func dropSignature(dropped []droppedMatch, suppressed suppressedRoutes) string {
	parts := make([]string, 0, len(dropped)+len(suppressed.WorkflowIDs))
	for _, drop := range dropped {
		parts = append(parts, drop.WorkflowID+"="+drop.Reason)
	}
	for _, id := range suppressed.WorkflowIDs {
		parts = append(parts, id+"=suppressed")
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// reportFullyDropped announces that a polled cell matched at least one route but
// had every match removed by a pre-dispatch guard, so no workflow will run and no
// instance will exist. Before #380 this outcome produced no log line at any level
// and (in queue mode) a dispatch job that reported `succeeded` with nothing to
// show for it, which made a permanently wedged task indistinguishable from an
// idle one.
//
// One INFO line names the cell, the task, every dropped workflow and its reason;
// a `dispatch.dropped` execution event per workflow makes the same fact queryable
// per task. Repeats are suppressed while the reason set is unchanged: an
// "active instance" drop is the normal state of a task whose workflow is still
// running, and re-logging it every poll interval would bury the signal.
//
// When the dropped set includes an exclusive winner, the line also names the
// lower-priority workflows that winner stopped the router from considering. Those
// are NOT re-routed to (that would duplicate the run the guard is protecting), so
// without naming them the operator sees "nothing will run" with no hint that a
// viable fallback exists one priority down.
func (d *Dispatcher) reportFullyDropped(ctx context.Context, cell model.SourceItem, task model.InternalTask, dropped []droppedMatch, suppressed suppressedRoutes) {
	signature := dropSignature(dropped, suppressed)
	if previous, seen := d.dropNotified.Load(task.ID); seen && previous == signature {
		return
	}
	d.dropNotified.Store(task.ID, signature)

	reasons := make([]string, 0, len(dropped))
	for _, drop := range dropped {
		reasons = append(reasons, drop.String())
	}
	suppressedNote := ""
	if len(suppressed.WorkflowIDs) > 0 {
		suppressedNote = fmt.Sprintf("; exclusive workflow %s had already suppressed %d lower-priority match(es) (%s), which are not reconsidered",
			suppressed.ExclusiveWorkflowID, len(suppressed.WorkflowIDs), strings.Join(suppressed.WorkflowIDs, ", "))
	}
	aplog.Info("cell %s (%q): task %s matched %d workflow(s) but every one was dropped before dispatch — %s%s; nothing will run for this task until the reason clears",
		cell.LogLabel(), cell.Title, task.ID, len(dropped), strings.Join(reasons, ", "), suppressedNote)

	for _, drop := range dropped {
		metadata := map[string]any{"reason": drop.Reason, "detail": drop.Detail, "source_item_id": cell.ID}
		if drop.WorkflowID == suppressed.ExclusiveWorkflowID && len(suppressed.WorkflowIDs) > 0 {
			metadata["suppressed"] = strings.Join(suppressed.WorkflowIDs, ",")
		}
		d.recordExecutionEvent(ctx, db.ExecutionEvent{Type: "dispatch.dropped", TaskID: task.ID, WorkflowID: drop.WorkflowID,
			Metadata: metadata})
	}
}

// dropOnceMatches removes matches whose trigger declared `once: true` and that
// already have a completed (done) instance for this task. It is the source-agnostic
// run-at-most-once guard: a decomposition / fan-out workflow whose source item (a
// spec issue) stays in its trigger set after succeeding would otherwise be
// re-dispatched on every poll, fanning out a duplicate set of children (issue
// #119). Only routes that opt in via `once` are checked; all others pass through.
// Fail-closed: on a query error the (once) match is dropped — skip this poll and
// retry next, rather than risk a duplicate fan-out.
func (d *Dispatcher) dropOnceMatches(ctx context.Context, taskID string, matches []router.Match) ([]router.Match, []droppedMatch) {
	out := make([]router.Match, 0, len(matches))
	var dropped []droppedMatch
	for _, m := range matches {
		if !m.Route.Once {
			out = append(out, m)
			continue
		}
		done, err := d.db.HasCompletedInstanceForRoute(ctx, taskID, m.Route.ID)
		if err != nil {
			aplog.Error("once check task %s workflow %s: %v — skipping this poll", taskID, m.Route.ID, err)
			dropped = append(dropped, droppedMatch{WorkflowID: m.Route.ID, Reason: "guard error", Detail: err.Error()})
			continue
		}
		if done {
			aplog.Debug("task %s: workflow %s already completed and is once-only, skipping re-dispatch", taskID, m.Route.ID)
			dropped = append(dropped, droppedMatch{WorkflowID: m.Route.ID, Reason: "once", Detail: "already completed"})
			continue
		}
		out = append(out, m)
	}
	return out, dropped
}

// dropCappedMatches removes matches whose (task, workflow) has reached the
// consecutive-failure cap (settings.max_attempts), so a workflow that keeps
// failing is not re-dispatched forever. It is the internal backstop that does
// not depend on source-side labels — it stops runaway loops even for workflows
// with no on_fail. When a match is capped, the workflow's on_fail hook is
// applied best-effort (so an issue parks to needs-attention and leaves the poll
// set when the workflow defines one). Fail-open: on a count error the match is
// kept (the cap is a safety net, not a correctness guard).
func (d *Dispatcher) dropCappedMatches(ctx context.Context, task model.InternalTask, matches []router.Match) ([]router.Match, []droppedMatch) {
	limit := d.cfg.Settings.MaxAttempts
	if limit <= 0 {
		return matches, nil
	}
	out := make([]router.Match, 0, len(matches))
	var dropped []droppedMatch
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
			dropped = append(dropped, droppedMatch{WorkflowID: m.Route.ID, Reason: "capped", Detail: fmt.Sprintf("%d/%d consecutive failures", n, limit)})
			continue
		}
		out = append(out, m)
	}
	return out, dropped
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

// agentTools is the fixed set of tool permissions written for an agent, in a
// stable order so generated files don't churn.
var agentTools = []string{"read", "glob", "grep", "task", "edit", "bash", "webfetch"}

// agentPermissions returns the OpenCode tool-permission map for an agent.
//
// The baseline is permissive (historical behaviour) unless
// settings.least_privilege_agents is set, which flips write/shell/network tools
// to deny. Either way an agent's explicit permissions win, so an operator can
// restrict a single agent without flipping the global default. Keeping the
// default permissive matters because these files are rewritten on every
// dispatch: a silent flip would leave existing agents unable to edit code, and
// they would fail by producing no diff rather than by erroring.
func agentPermissions(ac config.AgentConfig, leastPrivilege bool) map[string]string {
	perms := map[string]string{}
	for _, tool := range agentTools {
		perms[tool] = "allow"
	}
	if leastPrivilege {
		perms["edit"], perms["bash"], perms["webfetch"] = "deny", "deny", "deny"
	}
	for k, v := range ac.Permissions {
		perms[k] = v
	}
	return perms
}

// permissionMap converts a permission map to the any-typed form used in the JSON
// opencode config.
//
// Historically this writer emitted six keys and omitted webfetch, while the
// markdown writer emitted seven. Emitting webfetch here unconditionally would be
// a net privilege INCREASE on the default path — the wrong direction for a
// privilege-ceiling change — so under the permissive baseline the historical key
// set is preserved. An explicit agents[].permissions entry is always honoured.
func permissionMap(ac config.AgentConfig, leastPrivilege bool) map[string]any {
	perms := agentPermissions(ac, leastPrivilege)
	out := make(map[string]any, len(perms))
	for k, v := range perms {
		if k == "webfetch" && !leastPrivilege {
			if _, set := ac.Permissions[k]; !set {
				continue // preserve pre-existing opencode.json key set
			}
		}
		out[k] = v
	}
	return out
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
	perms := agentPermissions(ac, d.cfg.Settings.LeastPrivilegeAgents)
	for _, tool := range agentTools {
		fmt.Fprintf(&b, "  %s: %s\n", tool, perms[tool])
	}
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
		"permission":  permissionMap(ac, d.cfg.Settings.LeastPrivilegeAgents),
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

// injectRunnerSecurity folds a runner's sandbox and env-passthrough settings into
// the config map handed to the runner's Configure. The base map is never mutated;
// a copy is returned only when there is something to add.
func injectRunnerSecurity(base map[string]any, sandbox *config.SandboxConfig, envPassthrough []string) map[string]any {
	if sandbox == nil && len(envPassthrough) == 0 {
		return base
	}
	out := make(map[string]any, len(base)+2)
	for k, v := range base {
		out[k] = v
	}
	if sandbox != nil {
		extra := make([]any, len(sandbox.ExtraArgs))
		for i, v := range sandbox.ExtraArgs {
			extra[i] = v
		}
		out["sandbox"] = map[string]any{
			"image":      sandbox.Image,
			"user":       sandbox.User,
			"network":    sandbox.Network,
			"extra_args": extra,
		}
	}
	if len(envPassthrough) > 0 {
		p := make([]any, len(envPassthrough))
		for i, v := range envPassthrough {
			p[i] = v
		}
		out["env_passthrough"] = p
	}
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
		if err := ra.Configure(injectRunnerSecurity(rc.Config, rc.Sandbox, rc.EnvPassthrough)); err != nil {
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
