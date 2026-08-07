// Package improve mines Apiary's own execution history into an evidence pack:
// a deterministic, LLM-free summary of how the configured workflows, steps and
// agents actually behaved over a window. The pack is the sole input to the
// advisor agent (see the self-improvement-advisor change), but it is useful on
// its own — `apiary improve --dump-evidence` is a diagnostic that needs no model.
//
// Everything in this package is computed in Go from the SQLite database and the
// on-disk transcripts. No LLM is involved in producing any number here, so the
// metrics are reproducible and testable.
package improve

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// MinRuns is the sample size below which a metric is reported but flagged
// LowConfidence. Findings drawn from three runs read exactly like findings drawn
// from three hundred once they reach a prompt, so the distinction is carried in
// the data rather than left to the reader.
const MinRuns = 5

// Focus narrows what the analysis optimises for. All is the default.
type Focus string

const (
	FocusAll         Focus = "all"
	FocusCost        Focus = "cost"
	FocusLatency     Focus = "latency"
	FocusReliability Focus = "reliability"
	FocusQuality     Focus = "quality"
)

// Scope restricts the analysis to a subset of the history. Empty slices mean
// "everything".
type Scope struct {
	Workflows []string `json:"workflows,omitempty"`
	Agents    []string `json:"agents,omitempty"`
	Focus     Focus    `json:"focus"`
}

// Window is the half-open time range [Start, End) the pack covers.
type Window struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// EvidencePack is the complete deterministic input to the advisor.
type EvidencePack struct {
	GeneratedAt time.Time `json:"generated_at"`
	Window      Window    `json:"window"`
	Scope       Scope     `json:"scope"`

	Workflows   []WorkflowMetrics   `json:"workflows"`
	Steps       []StepMetrics       `json:"steps"`
	Agents      []AgentMetrics      `json:"agents"`
	Waits       []WaitMetrics       `json:"waits"`
	Failures    []FailureCluster    `json:"failure_clusters"`
	Dead        DeadPaths           `json:"dead_paths"`
	Transcripts []TranscriptExcerpt `json:"transcripts,omitempty"`

	// Digest is a stable hash over the pack's content, excluding GeneratedAt and
	// the window bounds. Two runs over the same rows produce the same digest,
	// which is what makes an improvement run reproducible after the fact.
	Digest string `json:"digest"`
}

// StepMetrics aggregates every run of one step of one workflow.
type StepMetrics struct {
	WorkflowID string `json:"workflow_id"`
	StepID     string `json:"step_id"`
	AgentID    string `json:"agent_id,omitempty"`

	Runs           int `json:"runs"`
	Passed         int `json:"passed"`
	Failed         int `json:"failed"`
	Skipped        int `json:"skipped"`
	SkippedCached  int `json:"skipped_cached"`
	LowConfidence  bool `json:"low_confidence"`

	PassRate       float64 `json:"pass_rate"`
	FailRate       float64 `json:"fail_rate"`
	SkipRate       float64 `json:"skip_rate"`
	CachedSkipRate float64 `json:"cached_skip_rate"`

	DurationP50Ms int64 `json:"duration_p50_ms"`
	DurationP95Ms int64 `json:"duration_p95_ms"`

	MeanTokens    float64 `json:"mean_tokens"`
	TotalTokens   int64   `json:"total_tokens"`
	MeanCostUSD   float64 `json:"mean_cost_usd"`
	TotalCostUSD  float64 `json:"total_cost_usd"`
	MeanTurns     float64 `json:"mean_turns"`
	MeanToolCalls float64 `json:"mean_tool_calls"`

	// CacheReuseRatio is cache_read_tokens / input_tokens. A hot step with a low
	// ratio is re-sending a prompt prefix that keeps changing — usually a sign
	// the prompt embeds volatile context that could be hoisted or dropped.
	CacheReuseRatio float64 `json:"cache_reuse_ratio"`
	// PromptWeightRatio is mean input prompt bytes per output token. A large
	// prompt yielding a tiny output is a prompt-design smell.
	PromptWeightRatio float64 `json:"prompt_weight_ratio"`

	// Wall-clock attribution (issue #399): where the step's minutes actually
	// went. Shares of the attributed total, so they sum to ~1 for runners that
	// report timing and are all zero for those that don't. "Slow" means something
	// different for a step that is 80% tool waits than for one that is 80%
	// thinking, and only this split tells them apart.
	ThinkingShare float64 `json:"thinking_share"`
	WritingShare  float64 `json:"writing_share"`
	ToolWaitShare float64 `json:"tool_wait_share"`

	// FailoverRate is the share of runs that needed more than one runner attempt.
	FailoverRate float64        `json:"failover_rate"`
	FailureKinds map[string]int `json:"failure_kinds,omitempty"`

	// MaxTurnsSaturation is the share of runs that ended at exactly the agent's
	// configured max_turns cap — the signature of a step being cut off rather
	// than finishing. Zero when the agent has no cap.
	MaxTurnsSaturation float64 `json:"max_turns_saturation"`
}

// WorkflowMetrics aggregates every instance of one workflow.
type WorkflowMetrics struct {
	WorkflowID string `json:"workflow_id"`

	Instances     int            `json:"instances"`
	ByState       map[string]int `json:"by_state"`
	LowConfidence bool           `json:"low_confidence"`

	DurationP50Ms int64 `json:"duration_p50_ms"`
	DurationP95Ms int64 `json:"duration_p95_ms"`

	CostPerCompletedUSD float64 `json:"cost_per_completed_usd"`
	TotalCostUSD        float64 `json:"total_cost_usd"`

	// ReworkLoops are steps that ran more than once inside a single instance —
	// the direct signature of an on_fail/goto cycle.
	ReworkLoops []ReworkLoop `json:"rework_loops,omitempty"`
	// ParallelCandidates are adjacent sequential steps with no data dependency
	// between them, computed statically from the config.
	ParallelCandidates []StepPair `json:"parallel_candidates,omitempty"`
	// DeadSteps are configured steps with no runs in the window.
	DeadSteps []string `json:"dead_steps,omitempty"`
}

// ReworkLoop records a step that repeated within one instance.
type ReworkLoop struct {
	StepID string `json:"step_id"`
	// Instances is how many instances repeated this step at all.
	Instances int `json:"instances"`
	// TotalRepeats is the number of runs beyond the first, summed across
	// instances — the actual wasted work.
	TotalRepeats int `json:"total_repeats"`
	MaxRepeats   int `json:"max_repeats"`
	// WastedCostUSD is the cost of the repeat runs only.
	WastedCostUSD float64 `json:"wasted_cost_usd"`
}

// StepPair is an ordered pair of adjacent steps that could run concurrently.
type StepPair struct {
	First  string `json:"first"`
	Second string `json:"second"`
	Reason string `json:"reason"`
}

// AgentMetrics aggregates runner invocations grouped by agent, runner and model.
type AgentMetrics struct {
	AgentID string `json:"agent_id"`
	Runner  string `json:"runner,omitempty"`
	Model   string `json:"model,omitempty"`

	Runs          int     `json:"runs"`
	SuccessRate   float64 `json:"success_rate"`
	LowConfidence bool    `json:"low_confidence"`

	MeanDurationMs int64   `json:"mean_duration_ms"`
	MeanCostUSD    float64 `json:"mean_cost_usd"`
	TotalCostUSD   float64 `json:"total_cost_usd"`
	MeanTurns      float64 `json:"mean_turns"`

	ConfiguredMaxTurns int     `json:"configured_max_turns,omitempty"`
	MaxTurnsSaturation float64 `json:"max_turns_saturation"`

	FailureKinds map[string]int `json:"failure_kinds,omitempty"`
}

// WaitMetrics aggregates a wait_for step's polling behaviour.
type WaitMetrics struct {
	WorkflowID string `json:"workflow_id,omitempty"`
	StepID     string `json:"step_id"`

	Waits          int            `json:"waits"`
	TotalPolls     int            `json:"total_polls"`
	MeanPolls      float64        `json:"mean_polls"`
	MaxPolls       int            `json:"max_polls"`
	TerminalStatus map[string]int `json:"terminal_status,omitempty"`
	Timeouts       int            `json:"timeouts"`
}

// FailureCluster groups failures whose messages normalise to the same shape, so
// one recurring failure reads as a single line with a count rather than thirty
// near-identical lines.
type FailureCluster struct {
	Normalized string `json:"normalized"`
	Count      int    `json:"count"`
	Exemplar   string `json:"exemplar"`
	Agents     []string `json:"agents,omitempty"`
}

// DeadPaths lists configured things that never ran in the window. Config that
// never executes is either obsolete or silently broken, and both are worth
// surfacing.
type DeadPaths struct {
	Workflows []string `json:"workflows,omitempty"`
	Agents    []string `json:"agents,omitempty"`
	Fallbacks []string `json:"fallbacks,omitempty"`
}

// TranscriptExcerpt is a truncated agent session transcript attached to a
// hotspot, so the advisor can reason about instructions and not only numbers.
type TranscriptExcerpt struct {
	WorkflowID string `json:"workflow_id,omitempty"`
	StepID     string `json:"step_id,omitempty"`
	TaskID     string `json:"task_id"`
	File       string `json:"file"`
	// Outcome is "failed" or "passed" — each hotspot samples failures plus one
	// successful control, so the advisor can contrast them.
	Outcome string `json:"outcome"`
	Bytes   int    `json:"bytes"`
	Content string `json:"content"`
}

// computeDigest hashes the pack's content, excluding the fields that legitimately
// differ between two runs over identical data (generation time and the absolute
// window bounds). Slices are already ordered deterministically by the queries
// that build them.
func (p *EvidencePack) computeDigest() string {
	clone := *p
	clone.GeneratedAt = time.Time{}
	clone.Window = Window{}
	clone.Digest = ""

	raw, err := json.Marshal(clone)
	if err != nil {
		// Marshal of a plain struct tree cannot fail; degrade rather than panic
		// in a diagnostic command.
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// rate divides safely, returning 0 for an empty denominator.
func rate(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) / float64(total)
}

// mean divides safely, returning 0 for an empty denominator.
func mean(sum float64, n int) float64 {
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// percentile returns the p-th percentile (0..1) of an already-sorted slice using
// nearest-rank. SQLite has no percentile function, so this runs in Go over the
// ordered durations the query returns.
func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	// Nearest-rank: ceil(p * n), 1-indexed.
	rank := max(int(float64(len(sorted))*p+0.999999), 1)
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}
