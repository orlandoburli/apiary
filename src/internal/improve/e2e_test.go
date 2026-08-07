package improve

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercises the whole Phase 3 + 4 path on a real tree: a recommendation goes
// through the gate, gets rendered, and is written to disk.
func TestGateToApplyEndToEnd(t *testing.T) {
	root := t.TempDir()
	if err := exec.Command("git", "-C", root, "init", "-q").Run(); err != nil {
		t.Skipf("git unavailable: %v", err)
	}

	soul := filepath.Join(root, "engineer.md")
	if err := os.WriteFile(soul, []byte("you are an engineer\nbe careful\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(root, "apiary.yaml")
	body := `version: "1"
default_runner: claude
runners:
  - id: claude
    type: cli
    provider: claude
agents:
  - id: engineer
    model: sonnet
    soul_file: ` + soul + `
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	ws := &Workspace{Root: root, Files: []WorkspaceFile{
		{Path: "apiary.yaml", Kind: KindConfig},
		{Path: "engineer.md", Kind: KindSoul, Owner: "engineer"},
	}}

	analysis := Analysis{
		Findings: []Finding{{ID: "f1", Scope: "wf/step", Symptom: "agents skip the tests",
			Evidence: []string{"fail_rate=0.3 n=40"}, Severity: "high"}},
		Recommendations: []Recommendation{
			{
				ID: "r1", Addresses: []string{"f1"}, File: "engineer.md",
				Summary: "tell the agent to run the tests", Confidence: "medium",
				Patch: "--- a/engineer.md\n+++ b/engineer.md\n@@ -1,2 +1,3 @@\n you are an engineer\n be careful\n+always run the tests\n",
			},
			{
				// Prose hunk header — the shape a real advisor produced.
				ID: "r2", Addresses: []string{"f1"}, File: "apiary.yaml",
				Summary: "retarget the merge step",
				Patch:   "--- a/apiary.yaml\n+++ b/apiary.yaml\n@@ merge step @@\n-  agent: engineer\n+  agent: engineer-merge\n",
			},
		},
	}

	v := NewValidator(ws, cfgPath, nil)
	verdicts := v.Validate(analysis.Recommendations)

	accepted, rejected := Summarize(verdicts)
	if len(accepted) != 1 || len(rejected) != 1 {
		t.Fatalf("want 1 accepted and 1 rejected, got %d/%d", len(accepted), len(rejected))
	}
	if accepted[0].MachineChecked {
		t.Error("the accepted change is a soul file; it cannot be machine-checked")
	}

	out := RenderDiff(analysis, verdicts)
	if !strings.Contains(out, "agents skip the tests") {
		t.Error("the diff must carry the finding that justified it")
	}
	if !strings.Contains(out, "Could not be validated") {
		t.Error("the rejected patch needs its section")
	}

	res, err := Apply(verdicts, root)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, err := os.ReadFile(soul)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "you are an engineer\nbe careful\nalways run the tests\n" {
		t.Errorf("soul file = %q", got)
	}

	// The config was never touched, because its patch never passed.
	cfgAfter, _ := os.ReadFile(cfgPath)
	if string(cfgAfter) != body {
		t.Error("a rejected patch must not reach disk")
	}

	if res.ConfigChanged {
		t.Error("only a prose file changed; no restart reminder is warranted")
	}
	summary := res.Summary()
	if !strings.Contains(summary, "not machine-checked") {
		t.Errorf("the summary must flag the unverified prose edit:\n%s", summary)
	}

	// git sees exactly one modified file.
	diff, err := exec.Command("git", "-C", root, "diff", "--name-only").Output()
	if err == nil {
		names := strings.Fields(string(diff))
		if len(names) != 0 { // files are untracked in a fresh init, so this is informational
			t.Logf("git diff reports: %v", names)
		}
	}
}
