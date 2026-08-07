package improve

import (
	"fmt"
	"sort"
	"strings"
)

// Analysis is the advisor's structured output.
type Analysis struct {
	Findings        []Finding        `json:"findings"`
	Recommendations []Recommendation `json:"recommendations"`
}

// Finding is an observation with a metric behind it.
type Finding struct {
	ID       string   `json:"id"`
	Scope    string   `json:"scope"`
	Symptom  string   `json:"symptom"`
	Evidence []string `json:"evidence"`
	Severity string   `json:"severity"`
	Focus    string   `json:"focus,omitempty"`
	// LowConfidence marks a finding drawn from a thin sample.
	LowConfidence bool `json:"low_confidence,omitempty"`
}

// Recommendation proposes a change addressing one or more findings.
type Recommendation struct {
	ID             string   `json:"id"`
	Addresses      []string `json:"addresses"`
	File           string   `json:"file"`
	Summary        string   `json:"summary"`
	Rationale      string   `json:"rationale"`
	Confidence     string   `json:"confidence"`
	ExpectedEffect string   `json:"expected_effect,omitempty"`
	Patch          string   `json:"patch,omitempty"`
}

// outputContract is the APIARY_OUTPUT shape the advisor must emit. It is spelled
// out rather than derived from a schema struct so the example stays readable —
// the advisor is being asked for prose-heavy JSON, and a worked example teaches
// that better than a type listing.
const outputContract = "APIARY_OUTPUT: " + `{"findings":[{"id":"f1","scope":"workflow:<id>/step:<id>","symptom":"<what is wrong, in one sentence>","evidence":["<metric>=<value> n=<runs>"],"severity":"high|medium|low","focus":"cost|latency|reliability|quality","low_confidence":false}],"recommendations":[{"id":"r1","addresses":["f1"],"file":"<path within the workspace>","summary":"<the change, in one sentence>","rationale":"<why this addresses the finding>","confidence":"high|medium|low","expected_effect":"<quantified where possible>","patch":"<unified diff>"}]}`

// ComposePrompt builds the advisor's prompt: the evidence, the configuration
// that produced it, the history of what has already been tried, and the rules
// that keep the analysis honest.
func ComposePrompt(pack *EvidencePack, ws *Workspace, files []WorkspaceFile, k Knobs, prior []LedgerFinding) string {
	var b strings.Builder

	b.WriteString("# Task\n\n")
	b.WriteString("Analyse how this Apiary pipeline has actually been running and propose ")
	b.WriteString("configuration changes that make it cheaper, faster or more reliable.\n\n")
	b.WriteString("You are given two things: metrics computed from the execution history, ")
	b.WriteString("and the configuration files that produced them.\n\n")

	b.WriteString("# Rules\n\n")
	b.WriteString("1. Every finding must cite a metric from the evidence below. If the evidence does not support a claim, do not make it.\n")
	b.WriteString(fmt.Sprintf("2. Samples under %d runs are marked low_confidence. You may still report them, but say the sample is thin.\n", MinRuns))
	b.WriteString("3. Correlation is not causation. A step may be expensive because its work is genuinely hard. Where the same agent runs in more than one workflow, compare them before blaming configuration.\n")
	b.WriteString("4. Never propose a change to a secret: tokens, env values, credentials. They are redacted below and must stay that way.\n")
	b.WriteString("5. Prefer fewer, better findings. Three solid ones beat twelve guesses. If the window is too thin to conclude anything, say exactly that and stop.\n")
	b.WriteString("6. Patches must be real unified diffs against a file listed under \"Configuration\". See the patch rules below — a patch that does not parse is discarded, and the finding behind it loses its fix.\n\n")

	// The patch format has to be spelled out. Left implicit, an advisor will
	// write a header that names the section in prose — `@@ implementation/merge
	// step @@` — which is not a unified diff and cannot be applied. Observed on
	// the first real run: two of three patches were unusable for this reason.
	b.WriteString("# Patch rules\n\n")
	b.WriteString("- Start with `--- a/<path>` and `+++ b/<path>`, using a path exactly as it appears under \"Configuration\".\n")
	b.WriteString("- Every hunk header must carry line numbers: `@@ -<start>,<count> +<start>,<count> @@`. A header naming a section in words is not a diff and will be discarded.\n")
	b.WriteString("- Line numbers and context must match the file content shown below. Count the lines; do not estimate.\n")
	b.WriteString("- Every body line begins with a space (context), `-` (removal) or `+` (addition). Include at least three lines of surrounding context where they exist.\n")
	b.WriteString("- Hunks must appear in ascending line order and must not overlap.\n")
	b.WriteString("- One file per patch. A change spanning two files is two recommendations.\n")
	b.WriteString("- Do not propose a patch that depends on a file that does not exist yet. If a change needs a new file, say so in the rationale and omit the patch.\n\n")

	b.WriteString("# What tends to be worth looking at\n\n")
	b.WriteString("- **Rework loops** — a step running repeatedly inside one instance is an on_fail/goto cycle. The repeat runs are pure waste; the transcripts show why the step does not pass first time.\n")
	b.WriteString("- **max_turns saturation** — runs ending exactly at the cap were cut off, not finished.\n")
	b.WriteString("- **Low cache reuse on a hot step** — the prompt prefix keeps changing between runs, usually volatile context that could be hoisted out.\n")
	b.WriteString("- **Heavy prompt, small output** — many input bytes per output token suggests an inflated prompt.\n")
	b.WriteString("- **Wall-clock split** — a step dominated by tool waits has a different problem from one dominated by thinking.\n")
	b.WriteString("- **Dead paths** — config that never runs is obsolete or silently broken. Both are worth saying.\n")
	b.WriteString("- **Expensive waits** — hundreds of polls, or frequent timeouts, is wall-clock the config could avoid.\n\n")

	b.WriteString("# Evidence\n\n")
	b.WriteString(pack.RenderTables())

	b.WriteString("\n# Configuration\n\n")
	if len(ws.UnresolvedSkills) > 0 {
		b.WriteString("⚠ These skills are declared by agents but could not be located on disk, ")
		b.WriteString("so their instructions are NOT included below. Do not assume what they contain:\n")
		for _, s := range ws.UnresolvedSkills {
			b.WriteString("  - " + s + "\n")
		}
		b.WriteString("\n")
	}
	for _, f := range files {
		fmt.Fprintf(&b, "## %s (%s", f.Path, f.Kind)
		if f.Owner != "" {
			fmt.Fprintf(&b, ", %s", f.Owner)
		}
		b.WriteString(")\n\n```\n")
		b.WriteString(f.Content)
		if !strings.HasSuffix(f.Content, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("```\n\n")
	}

	if len(prior) > 0 {
		// Without this the advisor re-proposes the same change every run, and a
		// suggestion that was applied and did not help is indistinguishable from
		// one that was never tried.
		b.WriteString("# Already tried\n\n")
		b.WriteString("These changes were proposed and applied by earlier runs. Do not re-propose them. ")
		b.WriteString("If a problem they addressed is still visible in the evidence above, that is worth saying — ")
		b.WriteString("the fix did not work, and the reason matters more than another attempt at the same idea.\n\n")
		for _, f := range prior {
			fmt.Fprintf(&b, "- **%s** (%s, %s)", f.Symptom, f.Scope, f.State)
			if f.TargetFile != "" {
				fmt.Fprintf(&b, " — changed `%s`", f.TargetFile)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("# Output\n\n")
	b.WriteString("Emit exactly one line of the following form, as the last thing you produce:\n\n")
	b.WriteString(outputContract + "\n\n")
	b.WriteString("Findings may exist without recommendations — reporting a problem you cannot ")
	b.WriteString("confidently fix is useful. A recommendation without a patch is also fine when ")
	b.WriteString("the change needs human judgement; say so in the rationale.\n")

	return b.String()
}

// RenderTables renders the evidence as compact markdown tables. Tables beat raw
// JSON here on both token count and readability.
func (p *EvidencePack) RenderTables() string {
	var b strings.Builder

	fmt.Fprintf(&b, "Window: %s → %s\n\n", p.Window.Start.Format("2006-01-02"), p.Window.End.Format("2006-01-02"))

	b.WriteString("## Workflows\n\n")
	b.WriteString("| workflow | instances | states | p50 | p95 | total $ | rework $ |\n|---|---|---|---|---|---|---|\n")
	for _, w := range sortedWorkflows(p.Workflows) {
		rework := 0.0
		for _, r := range w.ReworkLoops {
			rework += r.WastedCostUSD
		}
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %.2f | %.2f |\n",
			w.WorkflowID, w.Instances, compactStates(w.ByState),
			dur(w.DurationP50Ms), dur(w.DurationP95Ms), w.TotalCostUSD, rework)
	}

	b.WriteString("\n## Steps\n\n")
	b.WriteString("| workflow/step | agent | runs | pass | fail | p50 | p95 | total $ | turns | cache | prompt/out | failover | thin |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, s := range sortedSteps(p.Steps) {
		thin := ""
		if s.LowConfidence {
			thin = "⚠"
		}
		fmt.Fprintf(&b, "| %s/%s | %s | %d | %.0f%% | %.0f%% | %s | %s | %.2f | %.0f | %.2f | %.0f | %.0f%% | %s |\n",
			s.WorkflowID, s.StepID, s.AgentID, s.Runs, s.PassRate*100, s.FailRate*100,
			dur(s.DurationP50Ms), dur(s.DurationP95Ms), s.TotalCostUSD,
			s.MeanTurns, s.CacheReuseRatio, s.PromptWeightRatio, s.FailoverRate*100, thin)
	}

	if loops := allReworkLoops(p.Workflows); len(loops) > 0 {
		b.WriteString("\n## Rework loops (a step repeating inside one instance)\n\n")
		b.WriteString("| workflow/step | instances looped | extra runs | worst | wasted $ |\n|---|---|---|---|---|\n")
		for _, l := range loops {
			fmt.Fprintf(&b, "| %s | %d | %d | %d | %.2f |\n",
				l.scope, l.ReworkLoop.Instances, l.ReworkLoop.TotalRepeats, l.ReworkLoop.MaxRepeats, l.ReworkLoop.WastedCostUSD)
		}
	}

	b.WriteString("\n## Agents\n\n")
	b.WriteString("| agent | runner | model | runs | success | total $ | mean turns | max_turns | at cap |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, a := range p.Agents {
		cap := "—"
		if a.ConfiguredMaxTurns > 0 {
			cap = fmt.Sprint(a.ConfiguredMaxTurns)
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %d | %.0f%% | %.2f | %.1f | %s | %.0f%% |\n",
			a.AgentID, a.Runner, a.Model, a.Runs, a.SuccessRate*100,
			a.TotalCostUSD, a.MeanTurns, cap, a.MaxTurnsSaturation*100)
	}

	if len(p.Waits) > 0 {
		b.WriteString("\n## Waits\n\n")
		b.WriteString("| workflow/step | waits | polls | mean | max | outcomes | timeouts |\n|---|---|---|---|---|---|---|\n")
		for _, w := range p.Waits {
			wf := w.WorkflowID
			if wf == "" {
				wf = "(pruned instance)"
			}
			fmt.Fprintf(&b, "| %s/%s | %d | %d | %.1f | %d | %s | %d |\n",
				wf, w.StepID, w.Waits, w.TotalPolls, w.MeanPolls, w.MaxPolls,
				compactStates(w.TerminalStatus), w.Timeouts)
		}
	}

	if len(p.Failures) > 0 {
		b.WriteString("\n## Failure clusters\n\n")
		b.WriteString("| count | agents | normalised message |\n|---|---|---|\n")
		for _, f := range p.Failures {
			fmt.Fprintf(&b, "| %d | %s | %s |\n", f.Count, strings.Join(f.Agents, ","),
				strings.ReplaceAll(truncate(f.Normalized, 160), "|", "\\|"))
		}
	}

	if d := p.Dead; len(d.Workflows)+len(d.Agents)+len(d.Fallbacks) > 0 {
		b.WriteString("\n## Never ran in this window\n\n")
		if len(d.Workflows) > 0 {
			fmt.Fprintf(&b, "- workflows: %s\n", strings.Join(d.Workflows, ", "))
		}
		if len(d.Agents) > 0 {
			fmt.Fprintf(&b, "- agents: %s\n", strings.Join(d.Agents, ", "))
		}
		if len(d.Fallbacks) > 0 {
			fmt.Fprintf(&b, "- fallback runners: %s\n", strings.Join(d.Fallbacks, ", "))
		}
	}

	if cands := allParallelCandidates(p.Workflows); len(cands) > 0 {
		b.WriteString("\n## Possibly parallelisable (static analysis, conservative)\n\n")
		for _, c := range cands {
			fmt.Fprintf(&b, "- %s: `%s` → `%s` (%s)\n", c.workflow, c.StepPair.First, c.StepPair.Second, c.StepPair.Reason)
		}
	}

	if len(p.Transcripts) > 0 {
		b.WriteString("\n## Transcript excerpts\n\n")
		for _, t := range p.Transcripts {
			fmt.Fprintf(&b, "### %s/%s — %s (task %s, %d bytes)\n\n```\n%s\n```\n\n",
				t.WorkflowID, t.StepID, t.Outcome, t.TaskID, t.Bytes, t.Content)
		}
	}

	return b.String()
}

type scopedLoop struct {
	scope string
	ReworkLoop
}

func allReworkLoops(ws []WorkflowMetrics) []scopedLoop {
	var out []scopedLoop
	for _, w := range ws {
		for _, l := range w.ReworkLoops {
			out = append(out, scopedLoop{scope: w.WorkflowID + "/" + l.StepID, ReworkLoop: l})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].WastedCostUSD > out[j].WastedCostUSD })
	return out
}

type scopedPair struct {
	workflow string
	StepPair
}

func allParallelCandidates(ws []WorkflowMetrics) []scopedPair {
	var out []scopedPair
	for _, w := range ws {
		for _, c := range w.ParallelCandidates {
			out = append(out, scopedPair{workflow: w.WorkflowID, StepPair: c})
		}
	}
	return out
}

func sortedWorkflows(ws []WorkflowMetrics) []WorkflowMetrics {
	out := make([]WorkflowMetrics, len(ws))
	copy(out, ws)
	sort.SliceStable(out, func(i, j int) bool { return out[i].TotalCostUSD > out[j].TotalCostUSD })
	return out
}

func sortedSteps(ss []StepMetrics) []StepMetrics {
	out := make([]StepMetrics, len(ss))
	copy(out, ss)
	sort.SliceStable(out, func(i, j int) bool { return out[i].TotalCostUSD > out[j].TotalCostUSD })
	return out
}

func compactStates(m map[string]int) string {
	if len(m) == 0 {
		return "—"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", k, m[k]))
	}
	return strings.Join(parts, " ")
}

// dur renders a millisecond duration in the largest unit that stays readable.
func dur(ms int64) string {
	switch {
	case ms <= 0:
		return "—"
	case ms < 1000:
		return fmt.Sprintf("%dms", ms)
	case ms < 60_000:
		return fmt.Sprintf("%.0fs", float64(ms)/1000)
	case ms < 3_600_000:
		return fmt.Sprintf("%.0fm", float64(ms)/60000)
	default:
		return fmt.Sprintf("%.1fh", float64(ms)/3_600_000)
	}
}
