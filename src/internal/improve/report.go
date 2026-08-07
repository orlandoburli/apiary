package improve

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// severityRank orders findings for display; unknown severities sort last.
var severityRank = map[string]int{"high": 0, "medium": 1, "low": 2}

// RenderReport turns an analysis into the markdown a human reads. Findings come
// first and carry their evidence, because a recommendation whose justification
// is out of sight cannot be reviewed.
func RenderReport(pack *EvidencePack, adv *Advisor, out *RunOutcome, effort Effort) string {
	var b strings.Builder

	b.WriteString("# Pipeline improvement report\n\n")
	fmt.Fprintf(&b, "%s → %s · effort `%s` · advisor `%s` (%s / %s)\n\n",
		pack.Window.Start.Format(time.DateOnly), pack.Window.End.Format(time.DateOnly),
		effort, adv.AgentID, adv.RunnerID, adv.Model)
	b.WriteString(pack.Summary() + "\n\n")

	findings := make([]Finding, len(out.Analysis.Findings))
	copy(findings, out.Analysis.Findings)
	sort.SliceStable(findings, func(i, j int) bool {
		ri, ok := severityRank[strings.ToLower(findings[i].Severity)]
		if !ok {
			ri = 3
		}
		rj, ok := severityRank[strings.ToLower(findings[j].Severity)]
		if !ok {
			rj = 3
		}
		return ri < rj
	})

	byFinding := map[string][]Recommendation{}
	var orphaned []Recommendation
	for _, r := range out.Analysis.Recommendations {
		if len(r.Addresses) == 0 {
			orphaned = append(orphaned, r)
			continue
		}
		for _, id := range r.Addresses {
			byFinding[id] = append(byFinding[id], r)
		}
	}

	if len(findings) == 0 {
		b.WriteString("No findings. The advisor did not identify anything worth changing.\n\n")
	} else {
		b.WriteString("## Findings\n\n")
		for _, f := range findings {
			sev := strings.ToUpper(f.Severity)
			if sev == "" {
				sev = "UNRATED"
			}
			fmt.Fprintf(&b, "### [%s] %s\n\n", sev, f.Symptom)
			fmt.Fprintf(&b, "**Scope:** `%s`", f.Scope)
			if f.Focus != "" {
				fmt.Fprintf(&b, " · **Focus:** %s", f.Focus)
			}
			if f.LowConfidence {
				b.WriteString(" · ⚠ **thin sample**")
			}
			b.WriteString("\n\n")
			if len(f.Evidence) > 0 {
				b.WriteString("**Evidence:**\n")
				for _, e := range f.Evidence {
					b.WriteString("- `" + e + "`\n")
				}
				b.WriteString("\n")
			}
			for _, r := range byFinding[f.ID] {
				writeRecommendation(&b, r)
			}
		}
	}

	if len(orphaned) > 0 {
		b.WriteString("## Recommendations without a stated finding\n\n")
		b.WriteString("These were proposed without citing a finding, so the evidence behind them is not shown. Treat them as suggestions rather than conclusions.\n\n")
		for _, r := range orphaned {
			writeRecommendation(&b, r)
		}
	}

	b.WriteString("---\n\n")
	b.WriteString(costLine(out))
	return b.String()
}

func writeRecommendation(b *strings.Builder, r Recommendation) {
	fmt.Fprintf(b, "**→ %s**\n\n", r.Summary)
	if r.File != "" {
		fmt.Fprintf(b, "- file: `%s`\n", r.File)
	}
	if r.Confidence != "" {
		fmt.Fprintf(b, "- confidence: %s\n", r.Confidence)
	}
	if r.ExpectedEffect != "" {
		fmt.Fprintf(b, "- expected effect: %s\n", r.ExpectedEffect)
	}
	if r.Rationale != "" {
		fmt.Fprintf(b, "\n%s\n", r.Rationale)
	}
	if r.Patch != "" {
		b.WriteString("\n<details><summary>proposed patch</summary>\n\n```diff\n")
		b.WriteString(r.Patch)
		if !strings.HasSuffix(r.Patch, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("```\n\n</details>\n")
	}
	b.WriteString("\n")
}

// costLine reports what the analysis itself cost. A tool that spends money to
// tell you how you spend money should say what it spent.
func costLine(out *RunOutcome) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Analysis took %s", out.Duration.Round(time.Second))
	if out.Usage.TotalTokens > 0 {
		fmt.Fprintf(&b, ", %d tokens", out.Usage.TotalTokens)
	}
	if out.Usage.CostUSD > 0 {
		fmt.Fprintf(&b, ", $%.4f", out.Usage.CostUSD)
	}
	if chain := out.DescribeAttempts(); chain != "" {
		fmt.Fprintf(&b, " · runner chain: %s", chain)
	}
	b.WriteString("\n")
	return b.String()
}
