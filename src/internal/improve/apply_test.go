package improve

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func acceptedVerdict(path, result string, added, removed int, machineChecked bool) Verdict {
	return Verdict{
		OK: true, Path: path, Result: result,
		Added: added, Removed: removed, MachineChecked: machineChecked,
		Recommendation: Recommendation{
			ID: "r-" + filepath.Base(path), File: filepath.Base(path),
			Summary: "change " + filepath.Base(path),
			Patch:   "--- a/x\n+++ b/x\n@@ -1,1 +1,1 @@\n-a\n+b\n",
		},
	}
}

func TestApplyWritesExactContent(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "apiary.yaml")
	if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := Apply([]Verdict{acceptedVerdict(target, "new content\n", 1, 1, true)}, root)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new content\n" {
		t.Errorf("file content = %q, want the patched result verbatim", got)
	}
	if len(res.Files) != 1 || res.Files[0].Path != "apiary.yaml" {
		t.Errorf("Files = %+v, want one relative path", res.Files)
	}
	if !res.ConfigChanged {
		t.Error("a machine-checked (config) file must set ConfigChanged")
	}
}

func TestApplySkipsRejectedAndAdvisory(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "apiary.yaml")
	if err := os.WriteFile(target, []byte("untouched\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rejected := acceptedVerdict(target, "should not be written\n", 1, 1, true)
	rejected.OK = false

	advisory := Verdict{OK: true, Recommendation: Recommendation{ID: "r2", Summary: "think about it"}}

	res, err := Apply([]Verdict{rejected, advisory}, root)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Files) != 0 {
		t.Errorf("nothing should have been written, got %+v", res.Files)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "untouched\n" {
		t.Errorf("a rejected verdict must not reach disk; file = %q", got)
	}
}

// Each patch was validated against the ORIGINAL file, so applying two to the
// same file in sequence would apply the second to content it never saw.
func TestApplyRefusesTwoPatchesToOneFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "apiary.yaml")
	if err := os.WriteFile(target, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	a := acceptedVerdict(target, "first\n", 1, 0, true)
	a.Recommendation.ID = "r1"
	b := acceptedVerdict(target, "second\n", 1, 0, true)
	b.Recommendation.ID = "r2"

	_, err := Apply([]Verdict{a, b}, root)
	if err == nil {
		t.Fatal("two patches to the same file must be refused")
	}
	if !strings.Contains(err.Error(), "r1") || !strings.Contains(err.Error(), "r2") {
		t.Errorf("the error should name both proposals, got: %v", err)
	}
	// Nothing may be written when the set is refused.
	got, _ := os.ReadFile(target)
	if string(got) != "original\n" {
		t.Errorf("a refused apply must leave the file untouched, got %q", got)
	}
}

func TestApplyPreservesFileMode(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "soul.md")
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Apply([]Verdict{acceptedVerdict(target, "new\n", 1, 1, false)}, root); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	st, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want the file's original 0644 preserved", st.Mode().Perm())
	}
}

func TestSummaryWarnsOutsideGit(t *testing.T) {
	root := t.TempDir() // not a git repo
	target := filepath.Join(root, "apiary.yaml")
	if err := os.WriteFile(target, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := Apply([]Verdict{acceptedVerdict(target, "y\n", 1, 1, true)}, root)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.VersionControlled {
		t.Fatal("a plain temp dir is not a git repository")
	}

	summary := res.Summary()
	if !strings.Contains(summary, "not a git repository") {
		t.Errorf("the summary must say the undo story does not hold here:\n%s", summary)
	}
	// It warns, it does not refuse — the file is written either way.
	if got, _ := os.ReadFile(target); string(got) != "y\n" {
		t.Error("apply must still write outside git; the warning is not a refusal")
	}
}

func TestSummaryInsideGitPointsAtGitCommands(t *testing.T) {
	root := t.TempDir()
	if err := exec.Command("git", "-C", root, "init", "-q").Run(); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	target := filepath.Join(root, "apiary.yaml")
	if err := os.WriteFile(target, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := Apply([]Verdict{acceptedVerdict(target, "y\n", 1, 1, true)}, root)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !res.VersionControlled {
		t.Fatal("an initialised repo must be detected")
	}
	summary := res.Summary()
	if !strings.Contains(summary, "git diff") || !strings.Contains(summary, "git checkout") {
		t.Errorf("the summary should point at git for review and undo:\n%s", summary)
	}
	if strings.Contains(summary, "not a git repository") {
		t.Error("no warning belongs here")
	}
}

func TestSummaryFlagsProseAndConfigConsequences(t *testing.T) {
	root := t.TempDir()
	soul := filepath.Join(root, "engineer.md")
	cfg := filepath.Join(root, "apiary.yaml")
	for _, p := range []string{soul, cfg} {
		if err := os.WriteFile(p, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	res, err := Apply([]Verdict{
		acceptedVerdict(soul, "prose\n", 2, 0, false),
		acceptedVerdict(cfg, "conf\n", 1, 1, true),
	}, root)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	summary := res.Summary()
	if !strings.Contains(summary, "not machine-checked") {
		t.Errorf("prose files must be called out:\n%s", summary)
	}
	if !strings.Contains(summary, "apiary restart") {
		t.Errorf("a config change must carry the restart reminder:\n%s", summary)
	}
	// Files are listed in a stable order.
	if idx1, idx2 := strings.Index(summary, "apiary.yaml"), strings.Index(summary, "engineer.md"); idx1 > idx2 {
		t.Error("applied files should be listed in sorted order")
	}
}

func TestSummaryWithNothingApplied(t *testing.T) {
	res := &ApplyResult{VersionControlled: true}
	if got := res.Summary(); !strings.Contains(got, "Nothing applied") {
		t.Errorf("Summary = %q", got)
	}
}

func TestConfirmationPromptNamesWhatChanges(t *testing.T) {
	verdicts := []Verdict{
		acceptedVerdict("/w/apiary.yaml", "x", 1, 1, true),
		acceptedVerdict("/w/engineer.md", "y", 2, 0, false),
	}
	got := ConfirmationPrompt(verdicts)

	for _, want := range []string{"2 change(s)", "apiary.yaml", "engineer.md", "1 are instruction files", "[y/N]"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q: %s", want, got)
		}
	}

	// Nothing to apply produces no prompt at all.
	if p := ConfirmationPrompt(nil); p != "" {
		t.Errorf("ConfirmationPrompt(nil) = %q, want empty", p)
	}
	rejected := []Verdict{{OK: false, Recommendation: Recommendation{Patch: "d"}}}
	if p := ConfirmationPrompt(rejected); p != "" {
		t.Errorf("a rejected-only set should produce no prompt, got %q", p)
	}
}

func TestIsGitRepo(t *testing.T) {
	plain := t.TempDir()
	if IsGitRepo(plain) {
		t.Error("a plain temp dir is not a git repo")
	}

	repo := t.TempDir()
	if err := exec.Command("git", "-C", repo, "init", "-q").Run(); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	if !IsGitRepo(repo) {
		t.Error("an initialised repo must be detected")
	}
	// A subdirectory is still inside the work tree.
	sub := filepath.Join(repo, "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if !IsGitRepo(sub) {
		t.Error("a subdirectory of a repo is inside the work tree")
	}
}
