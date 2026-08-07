package improve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
)

// gateFixture builds a minimal but real config tree: a loadable apiary.yaml, a
// soul file it references, and a workspace describing both.
func gateFixture(t *testing.T) (root, cfgPath string, ws *Workspace) {
	t.Helper()
	root = t.TempDir()

	soul := filepath.Join(root, "engineer.md")
	if err := os.WriteFile(soul, []byte("you are an engineer\nbe careful\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfgPath = filepath.Join(root, "apiary.yaml")
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
    max_workers: 5
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	ws = &Workspace{Root: root, Files: []WorkspaceFile{
		{Path: "apiary.yaml", Kind: KindConfig},
		{Path: "engineer.md", Kind: KindSoul, Owner: "engineer"},
	}}
	return root, cfgPath, ws
}

func TestGateAcceptsAValidConfigPatch(t *testing.T) {
	_, cfgPath, ws := gateFixture(t)
	v := NewValidator(ws, cfgPath, nil)

	rec := Recommendation{
		ID: "r1", File: "apiary.yaml", Summary: "cap turns",
		Patch: `--- a/apiary.yaml
+++ b/apiary.yaml
@@ -9,3 +9,4 @@ agents:
   - id: engineer
     model: sonnet
+    max_turns: 40
`,
	}
	// The hunk must match the fixture; compute it against the real content.
	rec.Patch = patchAfterLine(t, cfgPath, "    model: sonnet", "    max_turns: 40")

	got := v.Validate([]Recommendation{rec})[0]
	if !got.OK {
		t.Fatalf("want accepted, stopped at %s: %s", got.Reached, got.Reason)
	}
	if got.Reached != StageValidated {
		t.Errorf("Reached = %s, want validated", got.Reached)
	}
	if !got.MachineChecked {
		t.Error("a YAML patch must be marked machine-checked")
	}
	if !strings.Contains(got.Result, "max_turns: 40") {
		t.Error("the patched result should carry the change")
	}
}

// A soul edit clears only path + apply. Nothing about it can be validated
// mechanically, and the reviewer has to be told that.
func TestGateMarksProseAsUnverifiable(t *testing.T) {
	_, cfgPath, ws := gateFixture(t)
	v := NewValidator(ws, cfgPath, nil)

	rec := Recommendation{
		ID: "r1", File: "engineer.md", Summary: "add a rule",
		Patch: `--- a/engineer.md
+++ b/engineer.md
@@ -1,2 +1,3 @@
 you are an engineer
 be careful
+always run the tests
`,
	}
	got := v.Validate([]Recommendation{rec})[0]
	if !got.OK {
		t.Fatalf("a clean prose patch should be accepted, stopped at %s: %s", got.Reached, got.Reason)
	}
	if got.MachineChecked {
		t.Error("a markdown patch cannot be machine-checked and must not claim to be")
	}
}

func TestGateRejectsAPatchThatBreaksTheConfig(t *testing.T) {
	_, cfgPath, ws := gateFixture(t)
	v := NewValidator(ws, cfgPath, nil)

	// Point soul_file at something that does not exist: cfg.Validate checks it.
	rec := Recommendation{
		ID: "r1", File: "apiary.yaml", Summary: "break it",
		Patch: patchReplaceLine(t, cfgPath, "    soul_file: ", "    soul_file: /nonexistent/ghost.md"),
	}
	got := v.Validate([]Recommendation{rec})[0]
	if got.OK {
		t.Fatal("a patch pointing soul_file at a missing file must be rejected")
	}
	if got.Reached != StageConfig {
		t.Errorf("Reached = %s, want config (cfg.Validate stage)", got.Reached)
	}
}

func TestGateRejectsUnparseableYAML(t *testing.T) {
	_, cfgPath, ws := gateFixture(t)
	v := NewValidator(ws, cfgPath, nil)

	// An unterminated quote breaks the YAML parser on a single line, so the
	// patch itself stays well-formed and the failure is genuinely the config's.
	rec := Recommendation{
		ID: "r1", File: "apiary.yaml", Summary: "mangle",
		Patch: patchReplaceLine(t, cfgPath, "version: ", `version: "unterminated`),
	}
	got := v.Validate([]Recommendation{rec})[0]
	if got.OK {
		t.Fatal("a patch producing invalid YAML must be rejected")
	}
}

func TestGateRejectsExcludedAndUnknownTargets(t *testing.T) {
	root, cfgPath, ws := gateFixture(t)
	v := NewValidator(ws, cfgPath, nil)

	// A .env target is on the exclusion list.
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("TOKEN=abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	envPatch := Recommendation{
		ID: "r1", File: ".env", Summary: "sneak",
		Patch: "--- a/.env\n+++ b/.env\n@@ -1,1 +1,1 @@\n-TOKEN=abc\n+TOKEN=stolen\n",
	}
	if got := v.Validate([]Recommendation{envPatch})[0]; got.OK {
		t.Error("a patch targeting .env must be rejected")
	}

	// A file that exists but was never shown to the advisor.
	other := filepath.Join(root, "other.md")
	if err := os.WriteFile(other, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unknown := Recommendation{
		ID: "r2", File: "other.md", Summary: "unseen",
		Patch: "--- a/other.md\n+++ b/other.md\n@@ -1,1 +1,1 @@\n-x\n+y\n",
	}
	got := v.Validate([]Recommendation{unknown})[0]
	if got.OK {
		t.Error("a patch to a file the advisor was never shown must be rejected")
	}
	if !strings.Contains(got.Reason, "never shown") && !strings.Contains(got.Reason, "not a file") {
		t.Errorf("reason should explain, got: %s", got.Reason)
	}
}

func TestGateRejectsFileHeaderMismatch(t *testing.T) {
	_, cfgPath, ws := gateFixture(t)
	v := NewValidator(ws, cfgPath, nil)

	rec := Recommendation{
		ID: "r1", File: "engineer.md", Summary: "mismatch",
		Patch: "--- a/apiary.yaml\n+++ b/apiary.yaml\n@@ -1,1 +1,1 @@\n-version: \"1\"\n+version: \"2\"\n",
	}
	got := v.Validate([]Recommendation{rec})[0]
	if got.OK {
		t.Fatal("a recommendation whose stated file disagrees with the diff target must be rejected")
	}
}

// A recommendation with no patch is legitimate: proposing a change that needs
// human judgement is often the honest answer.
func TestGateAcceptsAdvisoryRecommendations(t *testing.T) {
	_, cfgPath, ws := gateFixture(t)
	v := NewValidator(ws, cfgPath, nil)

	got := v.Validate([]Recommendation{{ID: "r1", Summary: "investigate the transcripts by hand"}})[0]
	if !got.OK {
		t.Error("a recommendation without a patch is advisory, not a failure")
	}
	if got.MachineChecked {
		t.Error("nothing was checked, so MachineChecked must be false")
	}
}

// If the patched file never reaches the validation mirror, cfg.Validate would
// run against the ORIGINAL content and pass — reporting a proposal as verified
// when nothing about it was checked.
func TestGateRefusesWhenTheTargetIsOutsideTheConfigTree(t *testing.T) {
	root, cfgPath, ws := gateFixture(t)

	// A YAML file that is in the workspace but not under the config directory.
	outside := t.TempDir()
	stray := filepath.Join(outside, "extra.yaml")
	if err := os.WriteFile(stray, []byte("a: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws.Files = append(ws.Files, WorkspaceFile{Path: stray, Kind: KindWorkflow})
	ws.Root = root

	v := NewValidator(ws, cfgPath, nil)
	rec := Recommendation{
		ID: "r1", File: stray, Summary: "stray",
		Patch: "--- a/" + stray + "\n+++ b/" + stray + "\n@@ -1,1 +1,1 @@\n-a: 1\n+a: 2\n",
	}
	got := v.Validate([]Recommendation{rec})[0]
	if got.OK {
		t.Fatal("a YAML target outside the config tree cannot be validated and must be refused, not silently passed")
	}
}

func TestSummarizeSplitsAcceptedAndRejected(t *testing.T) {
	verdicts := []Verdict{
		{OK: true, Recommendation: Recommendation{ID: "a"}},
		{OK: false, Recommendation: Recommendation{ID: "b"}, Reason: "nope"},
		{OK: true, Recommendation: Recommendation{ID: "c"}},
	}
	accepted, rejected := Summarize(verdicts)
	if len(accepted) != 2 || len(rejected) != 1 {
		t.Errorf("Summarize = %d accepted, %d rejected; want 2, 1", len(accepted), len(rejected))
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

// patchAfterLine builds a valid unified diff inserting a line after the first
// occurrence of anchor, computing real line numbers from the file.
func patchAfterLine(t *testing.T, path, anchor, insert string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	for i, l := range lines {
		if l == anchor {
			return "--- a/" + filepath.Base(path) + "\n+++ b/" + filepath.Base(path) + "\n" +
				"@@ -" + itoa(i+1) + ",1 +" + itoa(i+1) + ",2 @@\n" +
				" " + anchor + "\n+" + insert + "\n"
		}
	}
	t.Fatalf("anchor %q not found in %s", anchor, path)
	return ""
}

// patchReplaceLine builds a diff replacing the first line starting with prefix.
func patchReplaceLine(t *testing.T, path, prefix, replacement string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return "--- a/" + filepath.Base(path) + "\n+++ b/" + filepath.Base(path) + "\n" +
				"@@ -" + itoa(i+1) + ",1 +" + itoa(i+1) + ",1 @@\n" +
				"-" + l + "\n+" + replacement + "\n"
		}
	}
	t.Fatalf("no line starting with %q in %s", prefix, path)
	return ""
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

var _ = config.Config{}
