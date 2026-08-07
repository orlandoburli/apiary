package improve

import (
	"strings"
	"testing"
)

func TestRenderDiffPairsHunksWithTheirEvidence(t *testing.T) {
	analysis := Analysis{
		Findings: []Finding{{
			ID: "f1", Scope: "impl/implement", Symptom: "loops in 29 of 155 instances",
			Evidence: []string{"rework_$=55.09 n=29", "extra_runs=61"},
		}},
	}
	verdicts := []Verdict{{
		OK: true, Reached: StageValidated, MachineChecked: true, Added: 3, Removed: 1,
		Recommendation: Recommendation{
			ID: "r1", Addresses: []string{"f1"}, File: "apiary.yaml",
			Summary: "gate the step", Confidence: "medium", ExpectedEffect: "~$55/fortnight",
			Patch: "--- a/apiary.yaml\n+++ b/apiary.yaml\n@@ -1,1 +1,1 @@\n-a\n+b\n",
		},
	}}

	got := RenderDiff(analysis, verdicts)

	// The change, the evidence for it, and the tally must travel together — a
	// diff read next to the number that motivated it can be judged on whether it
	// is warranted, not merely whether it looks sensible.
	for _, want := range []string{
		"apiary.yaml", "+3 −1", "gate the step",
		"loops in 29 of 155 instances", "rework_$=55.09 n=29",
		"confidence medium", "~$55/fortnight", "```diff",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered diff missing %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "validated:") {
		t.Error("a machine-checked file should say so")
	}
}

func TestRenderDiffFlagsProseAsUnverified(t *testing.T) {
	verdicts := []Verdict{{
		OK: true, MachineChecked: false, Added: 2,
		Recommendation: Recommendation{
			ID: "r1", File: "engineer.md", Summary: "add a rule",
			Patch: "--- a/engineer.md\n+++ b/engineer.md\n@@ -1,1 +1,2 @@\n a\n+b\n",
		},
	}}
	got := RenderDiff(Analysis{}, verdicts)
	if !strings.Contains(got, "nothing here can be validated mechanically") {
		t.Errorf("a prose patch must be labelled unverifiable:\n%s", got)
	}
}

func TestRenderDiffShowsRejectionsWithReasons(t *testing.T) {
	verdicts := []Verdict{{
		OK: false, Reached: StageApply, Reason: "hunk 1 context mismatch at line 4",
		Recommendation: Recommendation{ID: "r1", File: "apiary.yaml", Summary: "retarget the merge step"},
	}}
	got := RenderDiff(Analysis{}, verdicts)

	if !strings.Contains(got, "Could not be validated") {
		t.Error("rejected proposals need their own section")
	}
	for _, want := range []string{"retarget the merge step", "apply", "context mismatch"} {
		if !strings.Contains(got, want) {
			t.Errorf("rejection should carry %q:\n%s", want, got)
		}
	}
	// A rejected patch is still a signal; the reader should be told it may be
	// worth acting on by hand.
	if !strings.Contains(got, "worth acting on by hand") {
		t.Error("rejections should say the observation may still matter")
	}
}

func TestRenderDiffSeparatesAdvisoryRecommendations(t *testing.T) {
	verdicts := []Verdict{{
		OK: true, Reason: "no patch — advisory only",
		Recommendation: Recommendation{ID: "r1", Summary: "audit the transcripts", Rationale: "cannot verify statically"},
	}}
	got := RenderDiff(Analysis{}, verdicts)
	if !strings.Contains(got, "Advisory (no patch proposed)") {
		t.Errorf("advisory recommendations need their own section:\n%s", got)
	}
	if !strings.Contains(got, "audit the transcripts") {
		t.Error("advisory content should be shown")
	}
}

func TestRenderDiffHandlesNothingApplicable(t *testing.T) {
	got := RenderDiff(Analysis{}, nil)
	if !strings.Contains(got, "No applicable changes") {
		t.Errorf("an empty verdict set should say so plainly:\n%s", got)
	}
}

func TestDiffSummaryCounts(t *testing.T) {
	verdicts := []Verdict{
		{OK: true, MachineChecked: true, Recommendation: Recommendation{Patch: "d"}},
		{OK: true, MachineChecked: false, Recommendation: Recommendation{Patch: "d"}},
		{OK: true, Recommendation: Recommendation{}}, // advisory
		{OK: false, Recommendation: Recommendation{Patch: "d"}},
	}
	got := DiffSummary(verdicts)
	for _, want := range []string{"2 change(s)", "1 machine-checked", "1 advisory", "1 rejected"} {
		if !strings.Contains(got, want) {
			t.Errorf("DiffSummary = %q, missing %q", got, want)
		}
	}
}

func TestNewWarningsAreSurfaced(t *testing.T) {
	verdicts := []Verdict{{
		OK: true, MachineChecked: true,
		NewWarnings: []string{"workflow x: step y is unreachable"},
		Recommendation: Recommendation{
			ID: "r1", File: "apiary.yaml", Summary: "reorder",
			Patch: "--- a/apiary.yaml\n+++ b/apiary.yaml\n@@ -1,1 +1,1 @@\n-a\n+b\n",
		},
	}}
	got := RenderDiff(Analysis{}, verdicts)
	if !strings.Contains(got, "introduces a new config warning") {
		t.Errorf("a warning the patch introduces must be shown beside it:\n%s", got)
	}
}
