package improve

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Delta is one metric measured before and after an applied change.
type Delta struct {
	Metric string
	Before float64
	After  float64
	// Unit shapes rendering: "usd", "pct", "ms" or "" for a bare count.
	Unit string
	// LowerIsBetter says which direction counts as improvement, so the renderer
	// does not have to guess per metric.
	LowerIsBetter bool
}

// Change reports the improvement, as a signed fraction where positive is better.
func (d Delta) Change() float64 {
	if d.Before == 0 {
		return 0
	}
	raw := (d.After - d.Before) / d.Before
	if d.LowerIsBetter {
		return -raw
	}
	return raw
}

// Effect is the before/after comparison for one applied finding.
type Effect struct {
	Finding LedgerFinding
	Scope   string
	Deltas  []Delta
	// SampleBefore and SampleAfter are the run counts each side rests on. A
	// delta drawn from four runs is not evidence, and the renderer says so.
	SampleBefore int
	SampleAfter  int
	// Insufficient marks a comparison too thin to read.
	Insufficient bool
	// Note explains why a comparison could not be made at all.
	Note string
}

// MeasureEffect recomputes the metrics behind each applied finding over the
// window since it was applied, and compares them to the baseline captured when
// it was proposed.
//
// This is what makes the feature a loop rather than a report generator. It
// matters most for instruction edits: the validation gate cannot check a soul
// file at all, so measured effect is the only evidence that a prose change did
// what it claimed.
func MeasureEffect(ctx context.Context, db source, run *LedgerRun, findings []LedgerFinding, now time.Time) ([]Effect, error) {
	if run.AppliedAt == nil {
		return nil, fmt.Errorf("run %s was never applied, so there is nothing to measure", run.ID)
	}
	after := Window{Start: run.AppliedAt.UTC(), End: now.UTC()}

	steps, err := StepMetricsFor(ctx, db, after, Scope{})
	if err != nil {
		return nil, err
	}
	byScope := map[string]StepMetrics{}
	for _, s := range steps {
		byScope[fmt.Sprintf("workflow:%s/step:%s", s.WorkflowID, s.StepID)] = s
	}

	var out []Effect
	for _, f := range findings {
		if f.State != FindingApplied {
			continue
		}
		e := Effect{Finding: f, Scope: f.Scope}

		baseline, ok := BaselineFor(f)
		if !ok {
			e.Note = "no baseline metrics were recorded for this finding"
			out = append(out, e)
			continue
		}
		current, ok := byScope[normalizeScope(f.Scope)]
		if !ok {
			e.Note = "this scope has not run since the change was applied"
			e.SampleBefore = baseline.Runs
			out = append(out, e)
			continue
		}

		e.SampleBefore = baseline.Runs
		e.SampleAfter = current.Runs
		e.Insufficient = current.Runs < MinRuns
		e.Deltas = []Delta{
			{Metric: "fail rate", Before: baseline.FailRate, After: current.FailRate, Unit: "pct", LowerIsBetter: true},
			{Metric: "cost/run", Before: baseline.MeanCostUSD, After: current.MeanCostUSD, Unit: "usd", LowerIsBetter: true},
			{Metric: "p95 duration", Before: float64(baseline.DurationP95Ms), After: float64(current.DurationP95Ms), Unit: "ms", LowerIsBetter: true},
			{Metric: "turns/run", Before: baseline.MeanTurns, After: current.MeanTurns, LowerIsBetter: true},
			{Metric: "failover rate", Before: baseline.FailoverRate, After: current.FailoverRate, Unit: "pct", LowerIsBetter: true},
		}
		out = append(out, e)
	}
	return out, nil
}

// normalizeScope tolerates the scope shapes an advisor produces. It writes
// "workflow:x/step:y" when asked, but also "x/y" and occasionally
// "workflow:x step:y" — matching only the canonical form would silently drop
// findings from the comparison.
func normalizeScope(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "workflow:") && strings.Contains(s, "/step:") {
		return s
	}
	s = strings.ReplaceAll(s, " step:", "/step:")
	if strings.HasPrefix(s, "workflow:") && strings.Contains(s, "/step:") {
		return s
	}
	if wf, step, found := strings.Cut(s, "/"); found && !strings.Contains(s, ":") {
		return fmt.Sprintf("workflow:%s/step:%s", wf, step)
	}
	return s
}

// RenderEffect prints the before/after comparison.
func RenderEffect(run *LedgerRun, effects []Effect) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# Effect of improvement run %s\n\n", run.ID)
	if run.AppliedAt != nil {
		fmt.Fprintf(&b, "Applied %s · measuring everything since.\n\n",
			run.AppliedAt.Format(time.RFC3339))
	}

	if len(effects) == 0 {
		b.WriteString("No applied findings to measure.\n")
		return b.String()
	}

	for _, e := range effects {
		fmt.Fprintf(&b, "## %s\n\n", e.Scope)
		if e.Finding.Symptom != "" {
			fmt.Fprintf(&b, "_%s_\n\n", e.Finding.Symptom)
		}
		if e.Note != "" {
			fmt.Fprintf(&b, "%s\n\n", e.Note)
			continue
		}

		fmt.Fprintf(&b, "n: %d before → %d after\n\n", e.SampleBefore, e.SampleAfter)
		if e.Insufficient {
			// Reporting a percentage off three runs would be worse than
			// reporting nothing, because it reads as a result.
			fmt.Fprintf(&b, "⚠ Only %d run(s) since the change — too few to conclude anything. "+
				"The numbers below are shown for completeness, not as a result.\n\n", e.SampleAfter)
		}

		b.WriteString("| metric | before | after | change |\n|---|---|---|---|\n")
		for _, d := range e.Deltas {
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n",
				d.Metric, formatValue(d.Before, d.Unit), formatValue(d.After, d.Unit), formatChange(d))
		}
		b.WriteString("\n")

		if !e.Insufficient && !e.Finding.MachineChecked {
			// The whole reason this phase matters: nothing could check this edit
			// when it was made, so the numbers here are the first real evidence.
			b.WriteString("_This was an instruction change; nothing about it could be validated when it was applied. " +
				"These numbers are the first evidence either way._\n\n")
		}
	}
	return b.String()
}

func formatValue(v float64, unit string) string {
	switch unit {
	case "usd":
		return fmt.Sprintf("$%.3f", v)
	case "pct":
		return fmt.Sprintf("%.0f%%", v*100)
	case "ms":
		return dur(int64(v))
	default:
		return fmt.Sprintf("%.1f", v)
	}
}

func formatChange(d Delta) string {
	if d.Before == 0 && d.After == 0 {
		return "—"
	}
	if d.Before == 0 {
		return "n/a (no baseline)"
	}
	pct := d.Change() * 100
	switch {
	case pct > 1:
		return fmt.Sprintf("↓ %.0f%% better", pct)
	case pct < -1:
		return fmt.Sprintf("↑ %.0f%% worse", -pct)
	default:
		return "unchanged"
	}
}

// BaselineJSON captures the metrics behind a finding at proposal time, so the
// same numbers can be recomputed and compared later.
func BaselineJSON(pack *EvidencePack, scope string) string {
	want := normalizeScope(scope)
	for _, s := range pack.Steps {
		if fmt.Sprintf("workflow:%s/step:%s", s.WorkflowID, s.StepID) == want {
			raw, err := json.Marshal(s)
			if err != nil {
				return ""
			}
			return string(raw)
		}
	}
	return ""
}
