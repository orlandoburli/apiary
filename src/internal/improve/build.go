package improve

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"sort"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
)

// Options configures a single evidence-pack build.
type Options struct {
	DBPath  string
	LogDir  string
	Config  *config.Config
	Window  Window
	Scope   Scope
	Clock   func() time.Time

	// TranscriptsPerHotspot and HotspotLimit are 0 in the metrics-only path
	// (`--dump-evidence` at low effort); the advisor sets them from its effort level.
	HotspotLimit          int
	TranscriptsPerHotspot int
	TranscriptByteBudget  int
}

// OpenReadOnly opens the Apiary database for analysis without touching it. The
// improve command must never migrate, write or lock the database a running
// daemon depends on, so it opens its own read-only connection rather than going
// through db.Client (which initialises schema on open).
func OpenReadOnly(dbPath string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_time_format=sqlite",
		url.PathEscape(dbPath))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open database %s: %w", dbPath, err)
	}
	return db, nil
}

// Build assembles the complete evidence pack. Every number in the result is
// computed here in Go — no model is consulted, so the same database and window
// always produce the same pack (and the same digest).
func Build(ctx context.Context, db source, opts Options) (*EvidencePack, error) {
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}

	steps, err := StepMetricsFor(ctx, db, opts.Window, opts.Scope)
	if err != nil {
		return nil, err
	}
	workflows, err := WorkflowMetricsFor(ctx, db, opts.Window, opts.Scope)
	if err != nil {
		return nil, err
	}
	agents, err := AgentMetricsFor(ctx, db, opts.Window, opts.Scope)
	if err != nil {
		return nil, err
	}
	waits, err := WaitMetricsFor(ctx, db, opts.Window, opts.Scope)
	if err != nil {
		return nil, err
	}
	failures, err := FailureClustersFor(ctx, db, opts.Window, opts.Scope)
	if err != nil {
		return nil, err
	}

	pack := &EvidencePack{
		GeneratedAt: clock().UTC(),
		Window:      opts.Window,
		Scope:       opts.Scope,
		Steps:       steps,
		Workflows:   workflows,
		Agents:      agents,
		Waits:       waits,
		Failures:    failures,
	}

	// Config-dependent analysis. Without a config the pack is still valid — it
	// just cannot say what *should* have run but didn't.
	if opts.Config != nil {
		if err := annotateFromConfig(ctx, db, pack, opts); err != nil {
			return nil, err
		}
	}

	if opts.TranscriptsPerHotspot > 0 {
		hotspots := RankHotspots(pack.Steps, opts.HotspotLimit)
		excerpts, err := SampleTranscripts(ctx, db, opts.LogDir, opts.Window,
			hotspots, opts.TranscriptsPerHotspot, opts.TranscriptByteBudget)
		if err != nil {
			return nil, err
		}
		pack.Transcripts = excerpts
	}

	pack.Digest = pack.computeDigest()
	return pack, nil
}

// annotateFromConfig fills in everything that needs the configuration to
// interpret: dead paths, parallel candidates, and turn-cap saturation.
func annotateFromConfig(ctx context.Context, db source, pack *EvidencePack, opts Options) error {
	cfg := opts.Config

	ranWorkflows := map[string]bool{}
	ranSteps := map[string]map[string]bool{}
	for _, s := range pack.Steps {
		ranWorkflows[s.WorkflowID] = true
		if ranSteps[s.WorkflowID] == nil {
			ranSteps[s.WorkflowID] = map[string]bool{}
		}
		ranSteps[s.WorkflowID][s.StepID] = true
	}
	ranAgents := map[string]bool{}
	ranRunners := map[string]bool{}
	for _, a := range pack.Agents {
		ranAgents[a.AgentID] = true
		if a.Runner != "" {
			ranRunners[a.Runner] = true
		}
	}
	for _, w := range pack.Workflows {
		ranWorkflows[w.WorkflowID] = true
	}

	pack.Dead = DeadPathsFor(cfg, ranWorkflows, ranAgents, ranRunners)

	byID := map[string]config.WorkflowConfig{}
	for _, wf := range cfg.Workflows {
		byID[wf.ID] = wf
	}
	for i := range pack.Workflows {
		wf, ok := byID[pack.Workflows[i].WorkflowID]
		if !ok {
			continue // ran under a definition no longer in config
		}
		pack.Workflows[i].ParallelCandidates = ParallelCandidates(wf)
		pack.Workflows[i].DeadSteps = DeadStepsFor(wf, ranSteps[wf.ID])
	}

	caps := TurnCaps(cfg)
	saturation, err := TurnCapSaturationFor(ctx, db, opts.Window, opts.Scope, caps)
	if err != nil {
		return err
	}
	for i := range pack.Agents {
		id := pack.Agents[i].AgentID
		if c, ok := caps[id]; ok {
			pack.Agents[i].ConfiguredMaxTurns = c
			pack.Agents[i].MaxTurnsSaturation = saturation[id]
		}
	}
	// Propagate each step's agent saturation, so a step-level reader does not
	// have to cross-reference the agent table to notice a truncated step.
	for i := range pack.Steps {
		if v, ok := saturation[pack.Steps[i].AgentID]; ok {
			pack.Steps[i].MaxTurnsSaturation = v
		}
	}
	return nil
}

// ParseWindow resolves a --since duration string ("7d", "24h", "90m") into a
// window ending now. Days are supported because that is how retention and
// review periods are actually discussed; Go's ParseDuration stops at hours.
func ParseWindow(since string, now time.Time) (Window, error) {
	d, err := ParseDurationDays(since)
	if err != nil {
		return Window{}, err
	}
	return Window{Start: now.Add(-d).UTC(), End: now.UTC()}, nil
}

// ParseDurationDays extends time.ParseDuration with a "d" (day) suffix.
func ParseDurationDays(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if n := len(s); s[n-1] == 'd' {
		var days float64
		if _, err := fmt.Sscanf(s[:n-1], "%g", &days); err != nil {
			return 0, fmt.Errorf("invalid duration %q: %w", s, err)
		}
		if days <= 0 {
			return 0, fmt.Errorf("invalid duration %q: must be positive", s)
		}
		return time.Duration(days * 24 * float64(time.Hour)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", s, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("invalid duration %q: must be positive", s)
	}
	return d, nil
}

// Summary renders the one-paragraph headline a human wants before the JSON: how
// much history was covered and what the biggest cost centres are.
func (p *EvidencePack) Summary() string {
	var totalCost float64
	var totalRuns int
	for _, s := range p.Steps {
		totalCost += s.TotalCostUSD
		totalRuns += s.Runs
	}

	top := make([]StepMetrics, len(p.Steps))
	copy(top, p.Steps)
	sort.SliceStable(top, func(i, j int) bool { return top[i].TotalCostUSD > top[j].TotalCostUSD })
	if len(top) > 3 {
		top = top[:3]
	}

	out := fmt.Sprintf("%d step runs across %d workflows, $%.2f total, %s → %s",
		totalRuns, len(p.Workflows), totalCost,
		p.Window.Start.Format(time.DateOnly), p.Window.End.Format(time.DateOnly))
	if totalRuns < MinRuns {
		out += fmt.Sprintf("\n⚠ only %d runs in this window — too thin to draw conclusions from", totalRuns)
	}
	for _, s := range top {
		out += fmt.Sprintf("\n  %s/%s: $%.2f over %d runs, %.0f%% fail",
			s.WorkflowID, s.StepID, s.TotalCostUSD, s.Runs, s.FailRate*100)
	}
	return out
}
