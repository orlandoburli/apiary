package improve

import (
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/model"
)

func samplePack() *EvidencePack {
	return &EvidencePack{
		Window: testWindow(),
		Steps: []StepMetrics{{
			WorkflowID: "impl", StepID: "implement", AgentID: "engineer",
			Runs: 100, PassRate: 0.9, FailRate: 0.1, TotalCostUSD: 42.5,
			DurationP50Ms: 90_000, DurationP95Ms: 600_000, MeanTurns: 30,
			CacheReuseRatio: 0.95, PromptWeightRatio: 12, FailoverRate: 0.05,
		}},
		Workflows: []WorkflowMetrics{{
			WorkflowID: "impl", Instances: 50, ByState: map[string]int{"done": 45, "failed": 5},
			TotalCostUSD: 42.5,
			ReworkLoops: []ReworkLoop{{
				StepID: "implement", Instances: 12, TotalRepeats: 20, MaxRepeats: 4, WastedCostUSD: 8.25,
			}},
			ParallelCandidates: []StepPair{{First: "lint", Second: "typecheck", Reason: "no reference"}},
		}},
		Agents: []AgentMetrics{{
			AgentID: "engineer", Runner: "claude", Model: "sonnet", Runs: 100,
			SuccessRate: 0.9, TotalCostUSD: 42.5, MeanTurns: 30,
			ConfiguredMaxTurns: 30, MaxTurnsSaturation: 0.4,
		}},
		Failures: []FailureCluster{{Normalized: "task <n> timed out", Count: 7, Agents: []string{"engineer"}}},
		Dead:     DeadPaths{Workflows: []string{"legacy"}, Agents: []string{"ghost"}},
		Waits: []WaitMetrics{{
			WorkflowID: "impl", StepID: "check-ci", Waits: 30, TotalPolls: 700,
			MeanPolls: 23.3, MaxPolls: 200, Timeouts: 2,
			TerminalStatus: map[string]int{"passed": 28, "timeout": 2},
		}},
	}
}

func TestComposePromptCarriesEvidenceAndRules(t *testing.T) {
	ws := &Workspace{Root: "/repo"}
	files := []WorkspaceFile{{Path: "apiary.yaml", Kind: KindConfig, Content: "version: \"1\"\n"}}

	got := ComposePrompt(samplePack(), ws, files, EffortStandard.Expand(), nil)

	// The evidence must actually be present, not merely referenced.
	for _, want := range []string{
		"impl/implement", "engineer", "42.50", // step row
		"Rework loops", "8.25", // the rework table
		"check-ci",           // waits
		"task <n> timed out", // failure clusters
		"legacy", "ghost",    // dead paths
		"lint` → `typecheck", // parallel candidates
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt is missing evidence %q", want)
		}
	}

	// The rules that keep the analysis honest.
	for _, want := range []string{
		"must cite a metric", "low_confidence", "Correlation is not causation",
		"Never propose a change to a secret", "Three solid ones beat twelve guesses",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt is missing rule %q", want)
		}
	}

	// The output contract.
	if !strings.Contains(got, "APIARY_OUTPUT:") {
		t.Error("prompt must state the output sentinel")
	}
	if !strings.Contains(got, "apiary.yaml") {
		t.Error("prompt must inline the config file")
	}
}

func TestComposePromptWarnsAboutUnresolvedSkills(t *testing.T) {
	ws := &Workspace{Root: "/repo", UnresolvedSkills: []string{"deploying (declared by engineer)"}}
	got := ComposePrompt(samplePack(), ws, nil, EffortStandard.Expand(), nil)

	if !strings.Contains(got, "deploying (declared by engineer)") {
		t.Error("unresolved skills must be named in the prompt")
	}
	if !strings.Contains(got, "Do not assume what they contain") {
		t.Error("the advisor must be told not to guess at instructions it cannot see")
	}
}

// The prompt is the last place a credential should appear. Discover redacts
// config content on the way in; this guards the whole path end to end.
func TestComposePromptNeverCarriesASecret(t *testing.T) {
	raw := "agents:\n  - id: a\n    source_token: ghp_supersecret\n    env:\n      KEY: sk-live-xyz\n"
	files := []WorkspaceFile{{Path: "apiary.yaml", Kind: KindConfig, Content: RedactConfig(raw)}}

	got := ComposePrompt(samplePack(), &Workspace{}, files, EffortStandard.Expand(), nil)
	for _, secret := range []string{"ghp_supersecret", "sk-live-xyz"} {
		if strings.Contains(got, secret) {
			t.Errorf("secret %q reached the prompt", secret)
		}
	}
}

func TestRenderTablesHandlesEmptyPack(t *testing.T) {
	empty := &EvidencePack{Window: testWindow()}
	got := empty.RenderTables()
	if got == "" {
		t.Error("an empty pack should still render its headers rather than nothing")
	}
	if strings.Contains(got, "NaN") || strings.Contains(got, "+Inf") {
		t.Errorf("empty pack produced a degenerate number:\n%s", got)
	}
}

func TestDurRendersReadableUnits(t *testing.T) {
	cases := []struct{ ms int64; want string }{
		{0, "—"}, {-1, "—"}, {500, "500ms"}, {1500, "2s"},
		{90_000, "2m"}, {3_600_000, "1.0h"}, {9_000_000, "2.5h"},
	}
	for _, tc := range cases {
		if got := dur(tc.ms); got != tc.want {
			t.Errorf("dur(%d) = %q, want %q", tc.ms, got, tc.want)
		}
	}
}

func TestDecodeAnalysis(t *testing.T) {
	good := map[string]any{
		"findings": []any{map[string]any{
			"id": "f1", "scope": "workflow:x/step:y", "symptom": "fails often",
			"evidence": []any{"fail_rate=0.4 n=88"}, "severity": "high",
		}},
		"recommendations": []any{map[string]any{
			"id": "r1", "addresses": []any{"f1"}, "file": "apiary.yaml",
			"summary": "gate it", "confidence": "medium",
		}},
	}
	a, err := decodeAnalysis(good)
	if err != nil {
		t.Fatalf("decodeAnalysis: %v", err)
	}
	if len(a.Findings) != 1 || a.Findings[0].ID != "f1" {
		t.Errorf("findings not decoded: %+v", a.Findings)
	}
	if len(a.Recommendations) != 1 || a.Recommendations[0].Addresses[0] != "f1" {
		t.Errorf("recommendations not decoded: %+v", a.Recommendations)
	}

	// An advisor that returns a well-formed but empty result is a failure, not a
	// clean run — silence here would read as "nothing to improve".
	if _, err := decodeAnalysis(map[string]any{}); err == nil {
		t.Error("an empty analysis must be an error")
	}
	if _, err := decodeAnalysis(map[string]any{"findings": "not-a-list"}); err == nil {
		t.Error("a malformed analysis must be an error")
	}
}

func TestRenderReportGroupsRecommendationsUnderFindings(t *testing.T) {
	adv := &Advisor{AgentID: "improver", RunnerID: "claude", Model: "opus"}
	out := &RunOutcome{
		Analysis: Analysis{
			Findings: []Finding{
				{ID: "f1", Scope: "impl/implement", Symptom: "loops often", Severity: "high",
					Evidence: []string{"rework=20 n=50"}},
				{ID: "f2", Scope: "impl/lint", Symptom: "minor", Severity: "low", LowConfidence: true},
			},
			Recommendations: []Recommendation{
				{ID: "r1", Addresses: []string{"f1"}, File: "apiary.yaml",
					Summary: "add a gate", Rationale: "because", Confidence: "medium",
					Patch: "--- a\n+++ b\n"},
				{ID: "r2", Summary: "unattached idea"},
			},
		},
		Usage: model.Usage{TotalTokens: 1234, CostUSD: 0.42},
	}

	got := RenderReport(samplePack(), adv, out, EffortStandard)

	if !strings.Contains(got, "loops often") || !strings.Contains(got, "add a gate") {
		t.Error("finding and its recommendation must both appear")
	}
	// High severity sorts above low.
	if strings.Index(got, "loops often") > strings.Index(got, "minor") {
		t.Error("findings must be ordered by severity")
	}
	if !strings.Contains(got, "thin sample") {
		t.Error("a low-confidence finding must be marked")
	}
	// An unattached recommendation is shown, but separately and with a caveat —
	// it has no evidence behind it.
	if !strings.Contains(got, "Recommendations without a stated finding") {
		t.Error("orphaned recommendations need their own section")
	}
	if !strings.Contains(got, "unattached idea") {
		t.Error("orphaned recommendation should still be shown")
	}
	// The analysis reports its own cost.
	if !strings.Contains(got, "1234 tokens") || !strings.Contains(got, "$0.42") {
		t.Errorf("report must state what the analysis itself cost:\n%s", got)
	}
}

func TestRenderReportHandlesNoFindings(t *testing.T) {
	adv := &Advisor{AgentID: "improver", RunnerID: "claude", Model: "opus"}
	out := &RunOutcome{Analysis: Analysis{}}
	got := RenderReport(samplePack(), adv, out, EffortQuick)
	if !strings.Contains(got, "No findings") {
		t.Error("an empty analysis should say so plainly")
	}
}

func TestDescribeAttemptsNamesTheFallbackChain(t *testing.T) {
	out := &RunOutcome{Attempts: []AttemptRecord{
		{RunnerID: "claude", Failure: "rate_limited"},
		{RunnerID: "codex", Success: true},
	}}
	got := out.DescribeAttempts()
	if !strings.Contains(got, "claude (rate_limited)") || !strings.Contains(got, "codex") {
		t.Errorf("DescribeAttempts = %q, want the chain with its reason", got)
	}

	// A single successful attempt needs no chain line.
	single := &RunOutcome{Attempts: []AttemptRecord{{RunnerID: "claude", Success: true}}}
	if single.DescribeAttempts() != "" {
		t.Error("a single attempt should not render a chain")
	}
}
