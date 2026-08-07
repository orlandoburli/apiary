package improve

import (
	"fmt"
	"sort"
	"strings"
)

// RenderDiff presents validated proposals as a reviewable diff.
//
// Each hunk arrives with the finding and metric that justified it, grouped by
// file. A diff read on its own can only be judged on whether the change looks
// sensible; read next to the number that motivated it, it can be judged on
// whether it is warranted. That is the difference between reviewing a patch and
// reviewing an argument.
func RenderDiff(analysis Analysis, verdicts []Verdict) string {
	accepted, rejected := Summarize(verdicts)

	findingByID := map[string]Finding{}
	for _, f := range analysis.Findings {
		findingByID[f.ID] = f
	}

	byFile := map[string][]Verdict{}
	var order []string
	for _, v := range accepted {
		if strings.TrimSpace(v.Recommendation.Patch) == "" {
			continue
		}
		path := v.Recommendation.File
		if path == "" {
			path = "(unspecified)"
		}
		if _, seen := byFile[path]; !seen {
			order = append(order, path)
		}
		byFile[path] = append(byFile[path], v)
	}
	sort.Strings(order)

	var b strings.Builder

	if len(order) == 0 {
		b.WriteString("No applicable changes.\n\n")
	}

	for _, path := range order {
		vs := byFile[path]
		added, removed := 0, 0
		machineChecked := true
		for _, v := range vs {
			added += v.Added
			removed += v.Removed
			if !v.MachineChecked {
				machineChecked = false
			}
		}

		fmt.Fprintf(&b, "## %s  (+%d −%d)\n", path, added, removed)
		if machineChecked {
			b.WriteString("_validated: parses, config checks and expression lint pass_\n\n")
		} else {
			// Saying this plainly is the point. A reviewer needs to know which
			// hunks a machine agreed with and which merely applied cleanly.
			b.WriteString("_prose file — only checked that the patch applies; nothing here can be validated mechanically_\n\n")
		}

		for _, v := range vs {
			r := v.Recommendation
			fmt.Fprintf(&b, "**%s**\n\n", r.Summary)

			for _, id := range r.Addresses {
				f, ok := findingByID[id]
				if !ok {
					continue
				}
				fmt.Fprintf(&b, "> %s", f.Symptom)
				if f.LowConfidence {
					b.WriteString(" _(thin sample)_")
				}
				b.WriteString("\n")
				for _, e := range f.Evidence {
					fmt.Fprintf(&b, "> · `%s`\n", e)
				}
				b.WriteString("\n")
			}

			if r.Confidence != "" || r.ExpectedEffect != "" {
				parts := []string{}
				if r.Confidence != "" {
					parts = append(parts, "confidence "+r.Confidence)
				}
				if r.ExpectedEffect != "" {
					parts = append(parts, r.ExpectedEffect)
				}
				fmt.Fprintf(&b, "%s\n\n", strings.Join(parts, " · "))
			}

			for _, w := range v.NewWarnings {
				fmt.Fprintf(&b, "⚠ introduces a new config warning: %s\n\n", w)
			}

			b.WriteString("```diff\n")
			b.WriteString(strings.TrimRight(r.Patch, "\n"))
			b.WriteString("\n```\n\n")
		}
	}

	// Advisory recommendations: accepted, but carrying no patch.
	var advisory []Verdict
	for _, v := range accepted {
		if strings.TrimSpace(v.Recommendation.Patch) == "" {
			advisory = append(advisory, v)
		}
	}
	if len(advisory) > 0 {
		b.WriteString("## Advisory (no patch proposed)\n\n")
		for _, v := range advisory {
			fmt.Fprintf(&b, "- **%s**", v.Recommendation.Summary)
			if v.Recommendation.Rationale != "" {
				fmt.Fprintf(&b, "\n  %s", v.Recommendation.Rationale)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if len(rejected) > 0 {
		// A rejected patch is still a signal — the advisor saw something. Showing
		// why it failed lets the reader judge whether the underlying observation
		// is worth acting on by hand.
		b.WriteString("## Could not be validated\n\n")
		for _, v := range rejected {
			fmt.Fprintf(&b, "- **%s**\n  - target: `%s`\n  - stopped at: %s\n  - reason: %s\n",
				v.Recommendation.Summary, v.Recommendation.File, v.Reached, v.Reason)
		}
		b.WriteString("\nThese were not applied. The observation behind each may still be worth acting on by hand.\n\n")
	}

	return b.String()
}

// DiffSummary is the one-line tally printed after a run.
func DiffSummary(verdicts []Verdict) string {
	accepted, rejected := Summarize(verdicts)
	patched, advisory, machineChecked := 0, 0, 0
	for _, v := range accepted {
		if strings.TrimSpace(v.Recommendation.Patch) == "" {
			advisory++
			continue
		}
		patched++
		if v.MachineChecked {
			machineChecked++
		}
	}

	parts := []string{fmt.Sprintf("%d change(s)", patched)}
	if machineChecked < patched {
		parts = append(parts, fmt.Sprintf("%d machine-checked", machineChecked))
	}
	if advisory > 0 {
		parts = append(parts, fmt.Sprintf("%d advisory", advisory))
	}
	if len(rejected) > 0 {
		parts = append(parts, fmt.Sprintf("%d rejected", len(rejected)))
	}
	return strings.Join(parts, ", ")
}
