package improve

import (
	"regexp"
	"sort"
	"strings"

	"github.com/orlandoburli/apiary/internal/config"
)

// ── failure normalisation ────────────────────────────────────────────────────

// Volatile fragments are stripped in a fixed order — longest/most specific
// patterns first, so a UUID is not shredded into digit runs before it is
// recognised as a UUID.
var failureScrubbers = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`), "<uuid>"},
	{regexp.MustCompile(`\b[0-9A-HJKMNP-TV-Z]{26}\b`), "<ulid>"},
	{regexp.MustCompile(`\b[0-9a-fA-F]{7,40}\b`), "<hex>"},
	{regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}\S*`), "<time>"},
	{regexp.MustCompile(`(/[\w.@%+-]+){2,}/?`), "<path>"},
	{regexp.MustCompile(`https?://\S+`), "<url>"},
	{regexp.MustCompile(`\b\d+(\.\d+)?(ms|s|m|h)\b`), "<dur>"},
	{regexp.MustCompile(`\b\d+\b`), "<n>"},
	{regexp.MustCompile(`\s+`), " "},
}

// NormalizeFailure reduces an error message to its recurring shape by removing
// the parts that differ between two occurrences of the same failure: ids, paths,
// timestamps, durations and bare numbers. "task 4821 timed out after 1800s" and
// "task 9134 timed out after 900s" collapse to one cluster key.
func NormalizeFailure(msg string) string {
	s := strings.TrimSpace(msg)
	for _, sc := range failureScrubbers {
		s = sc.re.ReplaceAllString(s, sc.with)
	}
	return truncate(strings.TrimSpace(s), 300)
}

// ── static config analysis ───────────────────────────────────────────────────

// ParallelCandidates reports adjacent sequential agent steps that appear to have
// no data dependency, and so could plausibly run concurrently.
//
// This is deliberately conservative: a pair is emitted only when the later step
// provably does not reference the earlier one anywhere the engine could read it
// (prompt, condition, fail_when, memory config, env values, sub-workflow inputs).
// A missed opportunity costs nothing; a wrong one is a recommendation to
// parallelise steps that genuinely depend on each other, so uncertainty always
// resolves to silence.
func ParallelCandidates(wf config.WorkflowConfig) []StepPair {
	var out []StepPair

	for i := 0; i+1 < len(wf.Steps); i++ {
		first, second := wf.Steps[i], wf.Steps[i+1]

		// Only plain agent steps are considered. Control-flow steps (split,
		// foreach, approval, wait_for, sub-workflow) carry ordering semantics
		// that a static reference scan cannot reason about.
		if !isPlainAgentStep(first) || !isPlainAgentStep(second) {
			continue
		}
		// An explicit branch or edge on the first step means the order is
		// intentional, not incidental.
		if first.OnPass != nil || first.OnFail != nil || first.OnConflict != nil {
			continue
		}
		// A step that publishes or spawns has an effect the next step may observe
		// through the source rather than through memory. Not statically knowable.
		if hasSideEffect(first) || hasSideEffect(second) {
			continue
		}
		if referencesStep(second, first.ID) {
			continue
		}
		// A later step reading arbitrary prior output through a bare `outputs`
		// or `memory` reference is treated as a dependency, since the scan
		// cannot tell which producer it means.
		if readsSharedState(second) {
			continue
		}

		out = append(out, StepPair{
			First:  first.ID,
			Second: second.ID,
			Reason: "adjacent agent steps; the second makes no reference to the first",
		})
	}
	return out
}

func isPlainAgentStep(s config.StepConfig) bool {
	return (s.Type == "" || s.Type == "agent") && s.Agent != ""
}

func hasSideEffect(s config.StepConfig) bool {
	if s.Publish != "" && s.Publish != "off" {
		return true
	}
	return s.Spawn != "" || s.Materialize != ""
}

// referencesStep reports whether the step mentions the given step id anywhere
// the engine evaluates or interpolates.
func referencesStep(s config.StepConfig, id string) bool {
	if id == "" {
		return true // unknown producer: assume dependency
	}
	for _, field := range referenceFields(s) {
		if strings.Contains(field, id) {
			return true
		}
	}
	return false
}

// sharedStateRe matches a reference to prior state whose producer cannot be
// determined statically.
//
// Note this deliberately scans only the step's own authored text. Every agent
// step reads the memory document by default, so treating memory-read as a
// dependency would suppress every candidate and make the analysis useless. What
// distinguishes a real dependency is the step *saying* it consumes prior state.
var sharedStateRe = regexp.MustCompile(`\b(outputs|memory|steps)\b`)

func readsSharedState(s config.StepConfig) bool {
	for _, field := range referenceFields(s) {
		if sharedStateRe.MatchString(field) {
			return true
		}
	}
	return false
}

// referenceFields returns every string on a step that could carry a reference to
// an earlier step's output.
func referenceFields(s config.StepConfig) []string {
	fields := []string{s.Prompt, s.Condition, s.FailWhen, s.SummaryPrompt, s.Items, s.Message}
	for _, v := range s.Env {
		fields = append(fields, v)
	}
	for _, v := range s.With {
		if str, ok := v.(string); ok {
			fields = append(fields, str)
		}
	}
	return fields
}

// DeadPathsFor reports configured workflows, agents and fallback runners that
// produced no runs in the window. Config that never executes is either obsolete
// or silently broken; both are worth a look.
func DeadPathsFor(cfg *config.Config, ranWorkflows, ranAgents, ranRunners map[string]bool) DeadPaths {
	var d DeadPaths

	for _, wf := range cfg.Workflows {
		if !ranWorkflows[wf.ID] {
			d.Workflows = append(d.Workflows, wf.ID)
		}
	}
	for _, a := range cfg.Agents {
		if !ranAgents[a.ID] {
			d.Agents = append(d.Agents, a.ID)
		}
	}

	// A fallback that never fired is reported once, no matter how many agents
	// declare it — the interesting fact is that the chain is untested, not which
	// agent owns it.
	seen := map[string]bool{}
	for _, a := range cfg.Agents {
		for _, fb := range a.Fallbacks {
			if fb.Runner == "" || ranRunners[fb.Runner] || seen[fb.Runner] {
				continue
			}
			seen[fb.Runner] = true
			d.Fallbacks = append(d.Fallbacks, fb.Runner)
		}
	}
	for _, fb := range cfg.Settings.DefaultFallbacks {
		if fb.Runner == "" || ranRunners[fb.Runner] || seen[fb.Runner] {
			continue
		}
		seen[fb.Runner] = true
		d.Fallbacks = append(d.Fallbacks, fb.Runner)
	}

	sort.Strings(d.Workflows)
	sort.Strings(d.Agents)
	sort.Strings(d.Fallbacks)
	return d
}

// DeadStepsFor reports configured steps of one workflow that never ran.
func DeadStepsFor(wf config.WorkflowConfig, ran map[string]bool) []string {
	var out []string
	for _, s := range wf.Steps {
		if s.ID != "" && !ran[s.ID] {
			out = append(out, s.ID)
		}
	}
	sort.Strings(out)
	return out
}

// TurnCaps returns each agent's configured max_turns, skipping agents with no
// cap (0 means unlimited).
func TurnCaps(cfg *config.Config) map[string]int {
	out := map[string]int{}
	for _, a := range cfg.Agents {
		if a.MaxTurns > 0 {
			out[a.ID] = a.MaxTurns
		}
	}
	return out
}
