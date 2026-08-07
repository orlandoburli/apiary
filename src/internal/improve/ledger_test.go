package improve

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTestLedger(t *testing.T) *Ledger {
	t.Helper()
	l, err := OpenLedger(ctx(), filepath.Join(t.TempDir(), "apiary.db"))
	if err != nil {
		t.Fatalf("OpenLedger: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func sampleRun(id string, at time.Time) LedgerRun {
	return LedgerRun{
		ID: id, Effort: "standard", Focus: "all",
		WindowStart: at.Add(-14 * 24 * time.Hour), WindowEnd: at,
		EvidenceDigest: "abc123", AdvisorAgent: "improver",
		AdvisorRunner: "claude", AdvisorModel: "sonnet",
		CostUSD: 0.95, TotalTokens: 146815, CreatedAt: at,
	}
}

func TestLedgerRoundTrip(t *testing.T) {
	l := openTestLedger(t)
	at := base

	run := sampleRun("imp_1", at)
	findings := []LedgerFinding{
		{
			ID: FindingRowID("imp_1", "r1"), FindingID: "r1",
			Scope: "workflow:impl/step:implement", Severity: "high", Confidence: "medium",
			Symptom: "loops often", TargetFile: ".apiary/agents/engineer.md",
			BaselineMetrics: `{"runs":100,"fail_rate":0.3}`, Patch: "--- a\n+++ b\n",
			MachineChecked: false, State: FindingProposed,
		},
		{
			ID: FindingRowID("imp_1", "r2"), FindingID: "r2",
			Scope: "workflow:impl/step:merge", State: FindingRejected,
			RejectReason: "malformed hunk header",
		},
	}
	if err := l.RecordRun(ctx(), run, findings); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}

	gotRun, gotFindings, err := l.GetRun(ctx(), "imp_1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if gotRun.Effort != "standard" || gotRun.AdvisorModel != "sonnet" {
		t.Errorf("run round-trip lost fields: %+v", gotRun)
	}
	if gotRun.CostUSD != 0.95 || gotRun.TotalTokens != 146815 {
		t.Errorf("cost/token round-trip: %v / %d", gotRun.CostUSD, gotRun.TotalTokens)
	}
	if gotRun.Applied {
		t.Error("a fresh run must not be marked applied")
	}
	if len(gotFindings) != 2 {
		t.Fatalf("want 2 findings, got %d", len(gotFindings))
	}
	if gotFindings[0].RejectReason != "" && gotFindings[0].State != FindingRejected {
		t.Error("reject reason should only accompany a rejected finding")
	}
}

func TestLedgerMarkAppliedOnlyTouchesNamedFindings(t *testing.T) {
	l := openTestLedger(t)
	run := sampleRun("imp_2", base)
	findings := []LedgerFinding{
		{ID: FindingRowID("imp_2", "r1"), FindingID: "r1", Scope: "s1", State: FindingProposed},
		{ID: FindingRowID("imp_2", "r2"), FindingID: "r2", Scope: "s2", State: FindingRejected},
	}
	if err := l.RecordRun(ctx(), run, findings); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}

	// Only r1 reached disk.
	if err := l.MarkApplied(ctx(), "imp_2", []string{FindingRowID("imp_2", "r1")}, base); err != nil {
		t.Fatalf("MarkApplied: %v", err)
	}

	gotRun, gotFindings, err := l.GetRun(ctx(), "imp_2")
	if err != nil {
		t.Fatal(err)
	}
	if !gotRun.Applied || gotRun.AppliedAt == nil {
		t.Error("the run must be marked applied with a timestamp")
	}
	states := map[string]string{}
	for _, f := range gotFindings {
		states[f.FindingID] = f.State
	}
	if states["r1"] != FindingApplied {
		t.Errorf("r1 state = %q, want applied", states["r1"])
	}
	if states["r2"] != FindingRejected {
		t.Errorf("r2 state = %q, want it left rejected — it never reached disk", states["r2"])
	}
}

// Without prior findings the advisor re-proposes the same change every run, and
// a suggestion that was applied and did not help looks identical to one never
// tried.
func TestLedgerPriorFindingsReturnsOnlyAppliedOnes(t *testing.T) {
	l := openTestLedger(t)

	if err := l.RecordRun(ctx(), sampleRun("imp_a", base.Add(-48*time.Hour)), []LedgerFinding{
		{ID: "imp_a:r1", FindingID: "r1", Scope: "s1", Symptom: "old applied", State: FindingApplied},
		{ID: "imp_a:r2", FindingID: "r2", Scope: "s2", Symptom: "old rejected", State: FindingRejected},
		{ID: "imp_a:r3", FindingID: "r3", Scope: "s3", Symptom: "never applied", State: FindingProposed},
	}); err != nil {
		t.Fatal(err)
	}

	prior, err := l.PriorFindings(ctx(), 50)
	if err != nil {
		t.Fatalf("PriorFindings: %v", err)
	}
	if len(prior) != 1 {
		t.Fatalf("want only the applied finding, got %d: %+v", len(prior), prior)
	}
	if prior[0].Symptom != "old applied" {
		t.Errorf("prior = %q", prior[0].Symptom)
	}
}

func TestLedgerGetRunUnknownID(t *testing.T) {
	l := openTestLedger(t)
	if _, _, err := l.GetRun(ctx(), "nope"); err == nil {
		t.Error("an unknown run id must be an error")
	}
}

func TestLedgerListRunsNewestFirst(t *testing.T) {
	l := openTestLedger(t)
	for i, id := range []string{"imp_old", "imp_mid", "imp_new"} {
		r := sampleRun(id, base.Add(time.Duration(i)*time.Hour))
		if err := l.RecordRun(ctx(), r, nil); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := l.ListRuns(ctx(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 || runs[0].ID != "imp_new" {
		t.Errorf("want newest first, got %v", []string{runs[0].ID})
	}
}

func TestNewRunIDIsSortable(t *testing.T) {
	earlier := NewRunID(base)
	later := NewRunID(base.Add(time.Hour))
	if !(earlier < later) {
		t.Errorf("run ids must sort chronologically: %q vs %q", earlier, later)
	}
	if !strings.HasPrefix(earlier, "imp_") {
		t.Errorf("run id = %q, want an imp_ prefix", earlier)
	}
}

func TestBaselineJSONCapturesTheScopedStep(t *testing.T) {
	pack := &EvidencePack{Steps: []StepMetrics{
		{WorkflowID: "impl", StepID: "implement", Runs: 100, FailRate: 0.3, MeanCostUSD: 1.5},
		{WorkflowID: "impl", StepID: "review", Runs: 50},
	}}

	raw := BaselineJSON(pack, "workflow:impl/step:implement")
	if raw == "" {
		t.Fatal("want the metrics for the named scope")
	}
	var m StepMetrics
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("baseline is not decodable: %v", err)
	}
	if m.Runs != 100 || m.FailRate != 0.3 {
		t.Errorf("baseline captured the wrong step: %+v", m)
	}

	if got := BaselineJSON(pack, "workflow:impl/step:nonexistent"); got != "" {
		t.Errorf("an unknown scope should capture nothing, got %q", got)
	}
}

// An advisor writes the canonical form when asked, but also produces "x/y" and
// "workflow:x step:y". Matching only the canonical shape would silently drop
// findings from the comparison.
func TestNormalizeScopeTolerantOfAdvisorShapes(t *testing.T) {
	want := "workflow:impl/step:implement"
	for _, in := range []string{
		"workflow:impl/step:implement",
		"workflow:impl step:implement",
		"impl/implement",
		"  workflow:impl/step:implement  ",
	} {
		if got := normalizeScope(in); got != want {
			t.Errorf("normalizeScope(%q) = %q, want %q", in, got, want)
		}
	}
}
