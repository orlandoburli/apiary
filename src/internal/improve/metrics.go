package improve

import (
	"context"
	"database/sql"
	"fmt"
	apstate "github.com/orlandoburli/apiary/internal/state"
	"slices"
	"sort"
	"strings"
)

// source is the read-only database surface this package needs. *sql.DB satisfies
// it; tests pass a seeded in-memory database.
type source interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// hasColumn reports whether a table carries a column.
//
// This matters because the improve command opens the database READ-ONLY and so
// never runs migrations, unlike every other entry point. A database written by
// an older binary is missing the newer columns, and selecting one is a hard SQL
// error rather than a null — so any column added after the first release has to
// be probed before it is selected. Degrading to "that metric is unavailable" is
// correct here; refusing to analyse an older database is not.
func hasColumn(ctx context.Context, db source, table, column string) bool {
	rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid         int
			name, ctype string
			notnull, pk int
			dflt        sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false
		}
		if name == column {
			return true
		}
	}
	return false
}

// stepAccumulator collects one step's rows before they are folded into a
// StepMetrics. Durations are kept as a slice because percentiles need the full
// ordered set, not a running sum.
type stepAccumulator struct {
	workflowID string
	stepID     string
	agentID    string

	runs, passed, failed, skipped, cached int
	durations                             []int64

	tokens, inputTokens, cacheReadTokens int64
	thinkingMS, writingMS, toolWaitMS    int64
	otherMS                              int64
	promptBytes                          int64
	cost                                 float64
	turns, toolCalls                     int64
	outputTokens                         int64

	withTurns int // runs that reported a turn count, for saturation math
	turnCap   map[int64]int
}

// StepMetricsFor aggregates every step run in the window into per-(workflow,step)
// metrics. Percentiles are computed in Go: SQLite ships no percentile function,
// and pulling the ordered durations is cheap at these row counts.
//
// Failover rate and failure kinds come from task_executions, which holds one row
// per runner invocation — so a step that failed over twice contributes three
// execution rows to one step_runs row.
func StepMetricsFor(ctx context.Context, db source, w Window, sc Scope) ([]StepMetrics, error) {
	where, args := stepFilter(w, sc)

	// Wall-clock attribution arrived after the first release (#399). A database
	// written by an older binary has no such columns, and this command never
	// migrates, so select them only when they exist.
	timingCols := `0, 0, 0, 0`
	hasTiming := hasColumn(ctx, db, "step_runs", "time_thinking_ms")
	if hasTiming {
		timingCols = `COALESCE(sr.time_thinking_ms, 0),
		       COALESCE(sr.time_writing_ms, 0),
		       COALESCE(sr.time_tool_wait_ms, 0),
		       COALESCE(sr.time_model_ms, 0) + COALESCE(sr.time_other_ms, 0)`
	}

	rows, err := db.QueryContext(ctx, `
		SELECT wi.workflow_id,
		       sr.step_id,
		       COALESCE(sr.agent_id, ''),
		       sr.state,
		       COALESCE(sr.skipped_cached, 0),
		       CAST(ROUND(COALESCE((julianday(sr.finished_at) - julianday(sr.started_at)) * 86400000, 0)) AS INTEGER),
		       COALESCE(sr.total_tokens, 0),
		       COALESCE(sr.input_tokens, 0),
		       COALESCE(sr.output_tokens, 0),
		       COALESCE(sr.cache_read_tokens, 0),
		       -- Byte length, not character length: LENGTH() on TEXT counts
		       -- characters and stops at an embedded NUL, which would silently
		       -- zero the prompt weight of any prompt carrying one.
		       COALESCE(LENGTH(CAST(sr.input_prompt AS BLOB)), 0),
		       COALESCE(sr.cost_usd, 0),
		       COALESCE(sr.num_turns, 0),
		       COALESCE(sr.num_tool_calls, 0),
		       `+timingCols+`
		FROM step_runs sr
		JOIN workflow_instances wi ON wi.id = sr.workflow_instance_id
		`+where+`
		ORDER BY wi.workflow_id, sr.step_id, sr.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("query step runs: %w", err)
	}
	defer rows.Close()

	acc := map[string]*stepAccumulator{}
	var order []string

	for rows.Next() {
		var (
			wf, step, agent, state          string
			cachedFlag                      int
			durMs                           int64
			tokens, inTok, outTok, cacheTok int64
			promptLen                       int64
			cost                            float64
			turns, tools                    int64
			thinkMS, writeMS, waitMS, othMS int64
		)
		if err := rows.Scan(&wf, &step, &agent, &state, &cachedFlag, &durMs,
			&tokens, &inTok, &outTok, &cacheTok, &promptLen, &cost, &turns, &tools,
			&thinkMS, &writeMS, &waitMS, &othMS); err != nil {
			return nil, fmt.Errorf("scan step run: %w", err)
		}

		key := wf + "\x00" + step
		a := acc[key]
		if a == nil {
			a = &stepAccumulator{workflowID: wf, stepID: step, agentID: agent, turnCap: map[int64]int{}}
			acc[key] = a
			order = append(order, key)
		}
		if a.agentID == "" {
			a.agentID = agent
		}

		a.runs++
		// Normalize so a database holding either vocabulary counts the same
		// (#465): 'passed' and 'done' are one bucket, and 'skipped_cached'
		// became 'skipped' plus a reason. The cached arm keeps its original
		// precedence — the skipped_cached flag wins over the plain state — so
		// the totals are unchanged for any existing database.
		canon := apstate.Normalize(state)
		switch {
		case canon == apstate.Done:
			a.passed++
		case canon == apstate.Failed:
			a.failed++
		case state == "skipped_cached" || cachedFlag == 1:
			a.cached++
			a.skipped++
		case canon == apstate.Skipped:
			a.skipped++
		}

		if durMs > 0 {
			a.durations = append(a.durations, durMs)
		}
		a.tokens += tokens
		a.inputTokens += inTok
		a.outputTokens += outTok
		a.cacheReadTokens += cacheTok
		a.promptBytes += promptLen
		a.cost += cost
		a.turns += turns
		a.toolCalls += tools
		a.thinkingMS += thinkMS
		a.writingMS += writeMS
		a.toolWaitMS += waitMS
		a.otherMS += othMS
		if turns > 0 {
			a.withTurns++
			a.turnCap[turns]++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate step runs: %w", err)
	}

	failover, err := stepFailovers(ctx, db, w, sc)
	if err != nil {
		return nil, err
	}

	out := make([]StepMetrics, 0, len(order))
	for _, key := range order {
		a := acc[key]
		slices.Sort(a.durations)

		m := StepMetrics{
			WorkflowID:     a.workflowID,
			StepID:         a.stepID,
			AgentID:        a.agentID,
			Runs:           a.runs,
			Passed:         a.passed,
			Failed:         a.failed,
			Skipped:        a.skipped,
			SkippedCached:  a.cached,
			LowConfidence:  a.runs < MinRuns,
			PassRate:       rate(a.passed, a.runs),
			FailRate:       rate(a.failed, a.runs),
			SkipRate:       rate(a.skipped, a.runs),
			CachedSkipRate: rate(a.cached, a.runs),
			DurationP50Ms:  percentile(a.durations, 0.50),
			DurationP95Ms:  percentile(a.durations, 0.95),
			MeanTokens:     mean(float64(a.tokens), a.runs),
			TotalTokens:    a.tokens,
			MeanCostUSD:    mean(a.cost, a.runs),
			TotalCostUSD:   a.cost,
			MeanTurns:      mean(float64(a.turns), a.runs),
			MeanToolCalls:  mean(float64(a.toolCalls), a.runs),
		}
		if a.inputTokens > 0 {
			m.CacheReuseRatio = float64(a.cacheReadTokens) / float64(a.inputTokens)
		}
		if a.outputTokens > 0 {
			m.PromptWeightRatio = float64(a.promptBytes) / float64(a.outputTokens)
		}
		// Attributed wall clock. Runners that don't stream provider events report
		// nothing here, leaving all three shares at zero rather than a misleading
		// even split.
		if attributed := a.thinkingMS + a.writingMS + a.toolWaitMS + a.otherMS; attributed > 0 {
			m.ThinkingShare = float64(a.thinkingMS) / float64(attributed)
			m.WritingShare = float64(a.writingMS) / float64(attributed)
			m.ToolWaitShare = float64(a.toolWaitMS) / float64(attributed)
		}
		if f, ok := failover[key]; ok {
			m.FailoverRate = rate(f.multiAttempt, a.runs)
			if len(f.kinds) > 0 {
				m.FailureKinds = f.kinds
			}
		}
		out = append(out, m)
	}
	return out, nil
}

type failoverInfo struct {
	multiAttempt int
	kinds        map[string]int
}

// stepFailovers counts, per (workflow, step), how many logical step runs needed
// more than one runner invocation, and what the provider rejections were.
//
// The signal is the NUMBER OF EXECUTION ROWS for one (instance, step), not the
// `attempt` column. `attempt` is a per-task monotonic counter — beginExecution
// sets it to the task's last attempt + 1 so a task's history accumulates across
// steps — so the fifth step of a workflow carries attempt=5 having never failed
// over even once. Reading it as a retry count marks almost every step as a
// failover. One step run invoking the runner twice is what actually means the
// first invocation was rejected.
func stepFailovers(ctx context.Context, db source, w Window, sc Scope) (map[string]failoverInfo, error) {
	where, args := executionFilter(w, sc, "te")

	rows, err := db.QueryContext(ctx, `
		SELECT wi.workflow_id,
		       te.step_id,
		       COUNT(*) AS invocations,
		       COALESCE(GROUP_CONCAT(te.failure_kind), '')
		FROM task_executions te
		JOIN workflow_instances wi ON wi.id = te.workflow_instance_id
		`+where+`
		  AND te.step_id IS NOT NULL AND te.step_id != ''
		GROUP BY te.workflow_instance_id, te.step_id, wi.workflow_id
		ORDER BY wi.workflow_id, te.step_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("query step failovers: %w", err)
	}
	defer rows.Close()

	out := map[string]failoverInfo{}
	for rows.Next() {
		var wf, step, kinds string
		var invocations int
		if err := rows.Scan(&wf, &step, &invocations, &kinds); err != nil {
			return nil, fmt.Errorf("scan step failover: %w", err)
		}
		key := wf + "\x00" + step
		info, ok := out[key]
		if !ok {
			info = failoverInfo{kinds: map[string]int{}}
		}
		if invocations > 1 {
			info.multiAttempt++
		}
		for k := range strings.SplitSeq(kinds, ",") {
			k = strings.TrimSpace(k)
			if k == "" || k == "none" {
				continue
			}
			info.kinds[k]++
		}
		out[key] = info
	}
	return out, rows.Err()
}

// WorkflowMetricsFor aggregates workflow instances, their end-to-end duration
// and the cost of the steps beneath them.
func WorkflowMetricsFor(ctx context.Context, db source, w Window, sc Scope) ([]WorkflowMetrics, error) {
	where, args := instanceFilter(w, sc)

	rows, err := db.QueryContext(ctx, `
		SELECT wi.workflow_id,
		       wi.state,
		       CAST(ROUND(COALESCE((julianday(wi.updated_at) - julianday(wi.created_at)) * 86400000, 0)) AS INTEGER),
		       COALESCE((SELECT SUM(COALESCE(sr.cost_usd, 0)) FROM step_runs sr
		                 WHERE sr.workflow_instance_id = wi.id), 0)
		FROM workflow_instances wi
		`+where+`
		ORDER BY wi.workflow_id, wi.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("query workflow instances: %w", err)
	}
	defer rows.Close()

	type acc struct {
		instances int
		byState   map[string]int
		durations []int64
		cost      float64
		completed int
	}
	m := map[string]*acc{}
	var order []string

	for rows.Next() {
		var wf, state string
		var durMs int64
		var cost float64
		if err := rows.Scan(&wf, &state, &durMs, &cost); err != nil {
			return nil, fmt.Errorf("scan workflow instance: %w", err)
		}
		a := m[wf]
		if a == nil {
			a = &acc{byState: map[string]int{}}
			m[wf] = a
			order = append(order, wf)
		}
		a.instances++
		a.byState[state]++
		if isTerminal(state) && durMs > 0 {
			a.durations = append(a.durations, durMs)
		}
		a.cost += cost
		if state == "done" {
			a.completed++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workflow instances: %w", err)
	}

	loops, err := ReworkLoopsFor(ctx, db, w, sc)
	if err != nil {
		return nil, err
	}

	out := make([]WorkflowMetrics, 0, len(order))
	for _, wf := range order {
		a := m[wf]
		slices.Sort(a.durations)
		out = append(out, WorkflowMetrics{
			WorkflowID:          wf,
			Instances:           a.instances,
			ByState:             a.byState,
			LowConfidence:       a.instances < MinRuns,
			DurationP50Ms:       percentile(a.durations, 0.50),
			DurationP95Ms:       percentile(a.durations, 0.95),
			TotalCostUSD:        a.cost,
			CostPerCompletedUSD: mean(a.cost, a.completed),
			ReworkLoops:         loops[wf],
		})
	}
	return out, nil
}

// ReworkLoopsFor finds steps that ran more than once within a single workflow
// instance. That repetition is the direct signature of an on_fail/goto cycle,
// and the repeat runs are pure waste — work the pipeline paid for twice.
func ReworkLoopsFor(ctx context.Context, db source, w Window, sc Scope) (map[string][]ReworkLoop, error) {
	where, args := stepFilter(w, sc)

	rows, err := db.QueryContext(ctx, `
		SELECT wf, step_id, COUNT(*) AS instances, SUM(runs - 1) AS repeats,
		       MAX(runs - 1) AS max_repeats, SUM(repeat_cost) AS wasted
		FROM (
		  SELECT wi.workflow_id AS wf,
		         sr.step_id AS step_id,
		         sr.workflow_instance_id AS inst,
		         COUNT(*) AS runs,
		         COALESCE(SUM(sr.cost_usd), 0) - COALESCE(MAX(sr.cost_usd), 0) AS repeat_cost
		  FROM step_runs sr
		  JOIN workflow_instances wi ON wi.id = sr.workflow_instance_id
		  `+where+`
		  GROUP BY sr.workflow_instance_id, sr.step_id, wi.workflow_id
		  HAVING COUNT(*) > 1
		)
		GROUP BY wf, step_id
		ORDER BY wf, wasted DESC, step_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("query rework loops: %w", err)
	}
	defer rows.Close()

	out := map[string][]ReworkLoop{}
	for rows.Next() {
		var wf, step string
		var instances, repeats, maxRepeats int
		var wasted float64
		if err := rows.Scan(&wf, &step, &instances, &repeats, &maxRepeats, &wasted); err != nil {
			return nil, fmt.Errorf("scan rework loop: %w", err)
		}
		out[wf] = append(out[wf], ReworkLoop{
			StepID:        step,
			Instances:     instances,
			TotalRepeats:  repeats,
			MaxRepeats:    maxRepeats,
			WastedCostUSD: wasted,
		})
	}
	return out, rows.Err()
}

// AgentMetricsFor aggregates runner invocations by agent, runner and model. The
// same agent on two models is two rows — comparing them is often the point.
func AgentMetricsFor(ctx context.Context, db source, w Window, sc Scope) ([]AgentMetrics, error) {
	where, args := executionFilter(w, sc, "te")

	rows, err := db.QueryContext(ctx, `
		SELECT te.agent_id,
		       COALESCE(te.runner, ''),
		       COALESCE(te.model, ''),
		       COUNT(*),
		       SUM(CASE WHEN te.status = 'success' THEN 1 ELSE 0 END),
		       COALESCE(AVG(te.duration_ms), 0),
		       COALESCE(SUM(te.cost_usd), 0),
		       COALESCE(AVG(te.num_turns), 0),
		       COALESCE(GROUP_CONCAT(te.failure_kind), '')
		FROM task_executions te
		`+where+`
		GROUP BY te.agent_id, te.runner, te.model
		ORDER BY te.agent_id, te.runner, te.model`, args...)
	if err != nil {
		return nil, fmt.Errorf("query agent metrics: %w", err)
	}
	defer rows.Close()

	var out []AgentMetrics
	for rows.Next() {
		var (
			agent, runner, model, kinds string
			runs, success               int
			avgDur, totalCost, avgTurns float64
		)
		if err := rows.Scan(&agent, &runner, &model, &runs, &success,
			&avgDur, &totalCost, &avgTurns, &kinds); err != nil {
			return nil, fmt.Errorf("scan agent metrics: %w", err)
		}
		m := AgentMetrics{
			AgentID:        agent,
			Runner:         runner,
			Model:          model,
			Runs:           runs,
			SuccessRate:    rate(success, runs),
			LowConfidence:  runs < MinRuns,
			MeanDurationMs: int64(avgDur),
			MeanCostUSD:    mean(totalCost, runs),
			TotalCostUSD:   totalCost,
			MeanTurns:      avgTurns,
		}
		if counted := countKinds(kinds); len(counted) > 0 {
			m.FailureKinds = counted
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// TurnCapSaturationFor reports, per agent, the share of runs that ended at
// exactly a given turn count. The caller supplies the configured caps from
// config, because the database has no idea what max_turns was set to; a run
// stopping precisely at the cap is the signature of a step being cut off rather
// than finishing.
func TurnCapSaturationFor(ctx context.Context, db source, w Window, sc Scope, caps map[string]int) (map[string]float64, error) {
	if len(caps) == 0 {
		return nil, nil
	}
	where, args := executionFilter(w, sc, "te")

	rows, err := db.QueryContext(ctx, `
		SELECT te.agent_id, COALESCE(te.num_turns, 0), COUNT(*)
		FROM task_executions te
		`+where+`
		GROUP BY te.agent_id, te.num_turns`, args...)
	if err != nil {
		return nil, fmt.Errorf("query turn saturation: %w", err)
	}
	defer rows.Close()

	total := map[string]int{}
	atCap := map[string]int{}
	for rows.Next() {
		var agent string
		var turns, n int
		if err := rows.Scan(&agent, &turns, &n); err != nil {
			return nil, fmt.Errorf("scan turn saturation: %w", err)
		}
		total[agent] += n
		if c, ok := caps[agent]; ok && c > 0 && turns == c {
			atCap[agent] += n
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := map[string]float64{}
	for agent, n := range total {
		if caps[agent] > 0 {
			out[agent] = rate(atCap[agent], n)
		}
	}
	return out, nil
}

// WaitMetricsFor aggregates how much polling each wait_for step actually did. A
// wait that polls hundreds of times, or that usually ends in a timeout, is
// costing wall-clock the config could avoid.
func WaitMetricsFor(ctx context.Context, db source, w Window, sc Scope) ([]WaitMetrics, error) {
	where, args := waitFilter(w, sc)

	rows, err := db.QueryContext(ctx, `
		SELECT COALESCE(wi.workflow_id, ''), c.step_id,
		       c.workflow_instance_id, COUNT(*),
		       COALESCE((SELECT c2.status FROM ci_poll_checks c2
		                 WHERE c2.workflow_instance_id = c.workflow_instance_id
		                   AND c2.step_id = c.step_id
		                 ORDER BY c2.id DESC LIMIT 1), '')
		FROM ci_poll_checks c
		LEFT JOIN workflow_instances wi ON wi.id = c.workflow_instance_id
		`+where+`
		GROUP BY c.workflow_instance_id, c.step_id, wi.workflow_id
		ORDER BY wi.workflow_id, c.step_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("query wait metrics: %w", err)
	}
	defer rows.Close()

	type acc struct {
		workflowID string
		waits      int
		polls      int
		maxPolls   int
		terminal   map[string]int
		timeouts   int
	}
	m := map[string]*acc{}
	var order []string

	for rows.Next() {
		var wf, step, inst, terminal string
		var polls int
		if err := rows.Scan(&wf, &step, &inst, &polls, &terminal); err != nil {
			return nil, fmt.Errorf("scan wait metrics: %w", err)
		}
		key := wf + "\x00" + step
		a := m[key]
		if a == nil {
			a = &acc{workflowID: wf, terminal: map[string]int{}}
			m[key] = a
			order = append(order, key)
		}
		a.waits++
		a.polls += polls
		if polls > a.maxPolls {
			a.maxPolls = polls
		}
		if terminal != "" {
			a.terminal[terminal]++
			if terminal == "timeout" {
				a.timeouts++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]WaitMetrics, 0, len(order))
	for _, key := range order {
		a := m[key]
		step := key[strings.Index(key, "\x00")+1:]
		out = append(out, WaitMetrics{
			WorkflowID:     a.workflowID,
			StepID:         step,
			Waits:          a.waits,
			TotalPolls:     a.polls,
			MeanPolls:      mean(float64(a.polls), a.waits),
			MaxPolls:       a.maxPolls,
			TerminalStatus: a.terminal,
			Timeouts:       a.timeouts,
		})
	}
	return out, nil
}

// FailureClustersFor groups failed executions by their normalised error message.
// Thirty occurrences of the same failure with different task ids collapse into
// one cluster with a count and an exemplar.
func FailureClustersFor(ctx context.Context, db source, w Window, sc Scope) ([]FailureCluster, error) {
	where, args := executionFilter(w, sc, "te")

	rows, err := db.QueryContext(ctx, `
		SELECT COALESCE(te.error_message, ''), te.agent_id
		FROM task_executions te
		`+where+`
		  AND te.status = 'failed'
		  AND te.error_message IS NOT NULL AND te.error_message != ''
		ORDER BY te.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("query failures: %w", err)
	}
	defer rows.Close()

	type acc struct {
		count    int
		exemplar string
		agents   map[string]struct{}
	}
	m := map[string]*acc{}
	var order []string

	for rows.Next() {
		var msg, agent string
		if err := rows.Scan(&msg, &agent); err != nil {
			return nil, fmt.Errorf("scan failure: %w", err)
		}
		key := NormalizeFailure(msg)
		a := m[key]
		if a == nil {
			a = &acc{exemplar: truncate(msg, 500), agents: map[string]struct{}{}}
			m[key] = a
			order = append(order, key)
		}
		a.count++
		if agent != "" {
			a.agents[agent] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]FailureCluster, 0, len(order))
	for _, key := range order {
		a := m[key]
		agents := make([]string, 0, len(a.agents))
		for id := range a.agents {
			agents = append(agents, id)
		}
		sort.Strings(agents)
		out = append(out, FailureCluster{
			Normalized: key,
			Count:      a.count,
			Exemplar:   a.exemplar,
			Agents:     agents,
		})
	}
	// Most frequent first; ties broken by the normalised text so the ordering —
	// and therefore the pack digest — is stable across runs.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Normalized < out[j].Normalized
	})
	return out, nil
}

// isTerminal reports whether an instance state means "no more work will happen".
//
// Interruption counts: an orphaned instance never completes on its own, which is
// why it sits alongside done and failed here even though the canonical
// vocabulary files it under 'blocked' (#465).
func isTerminal(state string) bool {
	switch apstate.Normalize(state) {
	case apstate.Done, apstate.Failed, apstate.Canceled:
		return true
	}
	return state == "interrupted"
}

func countKinds(concat string) map[string]int {
	out := map[string]int{}
	for k := range strings.SplitSeq(concat, ",") {
		k = strings.TrimSpace(k)
		if k == "" || k == "none" {
			continue
		}
		out[k]++
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ── filters ──────────────────────────────────────────────────────────────────

// stepFilter builds the WHERE clause for step_runs joined to workflow_instances.
func stepFilter(w Window, sc Scope) (string, []any) {
	clauses := []string{"sr.started_at >= ?", "sr.started_at < ?"}
	args := []any{w.Start, w.End}

	if len(sc.Workflows) > 0 {
		clauses = append(clauses, "wi.workflow_id IN ("+placeholders(len(sc.Workflows))+")")
		args = append(args, toAny(sc.Workflows)...)
	}
	if len(sc.Agents) > 0 {
		clauses = append(clauses, "sr.agent_id IN ("+placeholders(len(sc.Agents))+")")
		args = append(args, toAny(sc.Agents)...)
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

// instanceFilter builds the WHERE clause for a bare workflow_instances scan.
func instanceFilter(w Window, sc Scope) (string, []any) {
	clauses := []string{"wi.created_at >= ?", "wi.created_at < ?"}
	args := []any{w.Start, w.End}

	if len(sc.Workflows) > 0 {
		clauses = append(clauses, "wi.workflow_id IN ("+placeholders(len(sc.Workflows))+")")
		args = append(args, toAny(sc.Workflows)...)
	}
	if len(sc.Agents) > 0 {
		// An instance is in scope when any of its steps ran on a scoped agent.
		clauses = append(clauses, "EXISTS (SELECT 1 FROM step_runs sr WHERE sr.workflow_instance_id = wi.id AND sr.agent_id IN ("+placeholders(len(sc.Agents))+"))")
		args = append(args, toAny(sc.Agents)...)
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

// executionFilter builds the WHERE clause for task_executions. The alias is
// passed in so the same builder serves queries that join workflow_instances and
// queries that do not.
func executionFilter(w Window, sc Scope, alias string) (string, []any) {
	clauses := []string{alias + ".started_at >= ?", alias + ".started_at < ?"}
	args := []any{w.Start, w.End}

	if len(sc.Agents) > 0 {
		clauses = append(clauses, alias+".agent_id IN ("+placeholders(len(sc.Agents))+")")
		args = append(args, toAny(sc.Agents)...)
	}
	if len(sc.Workflows) > 0 {
		clauses = append(clauses, "EXISTS (SELECT 1 FROM workflow_instances wi2 WHERE wi2.id = "+alias+".workflow_instance_id AND wi2.workflow_id IN ("+placeholders(len(sc.Workflows))+"))")
		args = append(args, toAny(sc.Workflows)...)
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

func waitFilter(w Window, sc Scope) (string, []any) {
	clauses := []string{"c.checked_at >= ?", "c.checked_at < ?"}
	args := []any{w.Start, w.End}

	if len(sc.Workflows) > 0 {
		clauses = append(clauses, "wi.workflow_id IN ("+placeholders(len(sc.Workflows))+")")
		args = append(args, toAny(sc.Workflows)...)
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

func placeholders(n int) string {
	if n <= 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
