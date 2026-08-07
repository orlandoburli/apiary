package improve

import (
	"context"
	"fmt"
	"strings"

	"github.com/orlandoburli/apiary/internal/config"
)

// Critique is a second opinion on one proposal.
type Critique struct {
	RecommendationID string `json:"recommendation_id"`
	// Verdict is "sound", "weak" or "wrong".
	Verdict string `json:"verdict"`
	// Objection states the strongest case against the proposal. Required even
	// when the verdict is "sound" — a critic that returns approval with no
	// reasoning has not done the work.
	Objection string `json:"objection"`
	// Alternative is a different reading of the same evidence, when one exists.
	Alternative string `json:"alternative,omitempty"`
}

// CritiqueSet is the critic's structured output.
type CritiqueSet struct {
	Critiques []Critique `json:"critiques"`
}

// criticPrompt asks for the case against each proposal.
//
// The critic exists mainly for prose edits. A YAML change is checked by the
// validation gate: it parses, it validates, its expressions lint. A soul-file
// edit clears none of that — nothing mechanical can tell a good instruction from
// a plausible-sounding one. Until the effect measurement in a later phase can
// score a change against the metric that motivated it, an adversarial reading is
// the only automated check those edits get.
func criticPrompt(analysis Analysis, verdicts []Verdict, workspace []WorkspaceFile) string {
	var b strings.Builder

	b.WriteString("# Task\n\nAnother analyst reviewed this pipeline's execution history and proposed the changes below. ")
	b.WriteString("Your job is to argue against them.\n\n")
	b.WriteString("For each proposal, state the strongest objection you can make. Consider:\n\n")
	b.WriteString("- Does the evidence actually support the conclusion, or only correlate with it?\n")
	b.WriteString("- Is the sample large enough to distinguish a pattern from noise?\n")
	b.WriteString("- Would the change plausibly make something else worse — a step slower, an agent more likely to give up, a failure mode moved rather than removed?\n")
	b.WriteString("- For an instruction change: would an agent reading the new text actually behave differently, or does it just read better to a human?\n")
	b.WriteString("- Is there a simpler explanation for the metric that the proposal does not address?\n\n")
	b.WriteString("Return \"sound\" only when you genuinely cannot mount an objection that survives inspection. ")
	b.WriteString("You must give reasoning either way: an approval with no argument behind it is not useful.\n\n")

	b.WriteString("# Proposals\n\n")
	findingByID := map[string]Finding{}
	for _, f := range analysis.Findings {
		findingByID[f.ID] = f
	}
	for _, v := range verdicts {
		if !v.OK {
			continue
		}
		r := v.Recommendation
		fmt.Fprintf(&b, "## %s — %s\n\n", r.ID, r.Summary)
		fmt.Fprintf(&b, "- target: %s\n- confidence claimed: %s\n", r.File, r.Confidence)
		if r.ExpectedEffect != "" {
			fmt.Fprintf(&b, "- expected effect: %s\n", r.ExpectedEffect)
		}
		if !v.MachineChecked {
			b.WriteString("- **prose file: nothing about this change can be checked mechanically**\n")
		}
		b.WriteString("\n")
		for _, id := range r.Addresses {
			if f, ok := findingByID[id]; ok {
				fmt.Fprintf(&b, "Evidence offered: %s\n", f.Symptom)
				for _, e := range f.Evidence {
					fmt.Fprintf(&b, "  - %s\n", e)
				}
				if f.LowConfidence {
					b.WriteString("  - (marked as a thin sample)\n")
				}
			}
		}
		if r.Rationale != "" {
			fmt.Fprintf(&b, "\nStated rationale: %s\n", r.Rationale)
		}
		if r.Patch != "" {
			fmt.Fprintf(&b, "\n```diff\n%s\n```\n", strings.TrimRight(r.Patch, "\n"))
		}
		b.WriteString("\n")
	}

	if len(workspace) > 0 {
		b.WriteString("# Current configuration\n\n")
		for _, f := range workspace {
			fmt.Fprintf(&b, "## %s\n\n```\n%s\n```\n\n", f.Path, f.Content)
		}
	}

	b.WriteString("# Output\n\n")
	b.WriteString("Emit exactly one line, as the last thing you produce:\n\n")
	b.WriteString(`APIARY_OUTPUT: {"critiques":[{"recommendation_id":"r1","verdict":"sound|weak|wrong","objection":"<the strongest case against it>","alternative":"<a different reading of the same evidence, if one exists>"}]}` + "\n")
	return b.String()
}

// RunCritic asks the advisor to argue against its own proposals and folds the
// result into the verdicts. A "wrong" verdict demotes the proposal out of the
// accepted set; "weak" keeps it but attaches the objection.
func RunCritic(ctx context.Context, cfg *config.Config, adv *Advisor, analysis Analysis, verdicts []Verdict, workspace []WorkspaceFile, k Knobs, workDir string) ([]Verdict, *RunOutcome, error) {
	anyAccepted := false
	for _, v := range verdicts {
		if v.OK && strings.TrimSpace(v.Recommendation.Patch) != "" {
			anyAccepted = true
			break
		}
	}
	if !anyAccepted {
		return verdicts, nil, nil
	}

	prompt := criticPrompt(analysis, verdicts, workspace)
	outcome, err := runStructured(ctx, cfg, adv, prompt, k, workDir)
	if err != nil {
		// A failed critic must not discard a valid analysis. Report it and leave
		// the proposals as they were, unannotated.
		return verdicts, outcome, err
	}

	var set CritiqueSet
	if err := decodeInto(outcome.Structured, &set); err != nil {
		return verdicts, outcome, fmt.Errorf("critic output did not match the expected shape: %w", err)
	}

	byID := map[string]Critique{}
	for _, c := range set.Critiques {
		byID[c.RecommendationID] = c
	}

	out := make([]Verdict, 0, len(verdicts))
	for _, v := range verdicts {
		c, ok := byID[v.Recommendation.ID]
		if !ok {
			out = append(out, v)
			continue
		}
		v.Critique = &c
		if strings.EqualFold(c.Verdict, "wrong") {
			v.OK = false
			v.Reached = StageCritic
			v.Reason = "rejected by the critic pass: " + c.Objection
		}
		out = append(out, v)
	}
	return out, outcome, nil
}
