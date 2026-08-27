package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile creates path (with parents) holding body.
func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestResolveFindsSkillDirectory is the happy path: `<dir>/<name>/SKILL.md`
// under any of the search directories resolves.
func TestResolveFindsSkillDirectory(t *testing.T) {
	for _, dir := range SearchDirs {
		t.Run(dir, func(t *testing.T) {
			root := t.TempDir()
			want := filepath.Join(root, filepath.FromSlash(dir), "skill-429-fixture", "SKILL.md")
			writeFile(t, want, "# fixture skill\n")

			res := Resolve(root, "skill-429-fixture")
			if !res.Found() {
				t.Fatalf("expected skill-429-fixture to resolve, got %+v", res)
			}
			if res.Path != want {
				t.Errorf("path = %q, want %q", res.Path, want)
			}
			if res.Reason() != "" {
				t.Errorf("Reason() on a resolved skill = %q, want empty", res.Reason())
			}
		})
	}
}

// TestResolveMissingSkillListsCandidates covers the silent-failure case from
// issue #429: nothing on disk, and the failure names every path tried.
func TestResolveMissingSkillListsCandidates(t *testing.T) {
	root := t.TempDir()

	res := Resolve(root, "no-such-skill-429")
	if res.Found() {
		t.Fatalf("expected no resolution, got %q", res.Path)
	}
	if len(res.Candidates) < len(SearchDirs) {
		t.Fatalf("candidates = %v, want at least one per search dir", res.Candidates)
	}
	reason := res.Reason()
	for _, dir := range SearchDirs {
		want := filepath.Join(root, filepath.FromSlash(dir), "no-such-skill-429", "SKILL.md")
		if !strings.Contains(reason, want) {
			t.Errorf("reason %q does not mention candidate %q", reason, want)
		}
	}
	if strings.Contains(reason, "did you mean") {
		t.Errorf("reason %q offers a flat-file hint with no flat file on disk", reason)
	}
}

// TestResolveFlatFileHint is the mistake users actually make: the skill written
// as `<name>.md` next to where `<name>/SKILL.md` was expected. It must not
// resolve (no runner loads it) and it must produce the "did you mean" hint.
func TestResolveFlatFileHint(t *testing.T) {
	root := t.TempDir()
	flat := filepath.Join(root, ".apiary", "skills", "skill-429-fixture.md")
	writeFile(t, flat, "# fixture skill\n")

	res := Resolve(root, "skill-429-fixture")
	if res.Found() {
		t.Fatalf("a flat <name>.md must not resolve, got %q", res.Path)
	}
	if res.FlatFile != flat {
		t.Errorf("FlatFile = %q, want %q", res.FlatFile, flat)
	}
	want := filepath.Join(root, ".apiary", "skills", "skill-429-fixture", "SKILL.md")
	reason := res.Reason()
	if !strings.Contains(reason, "did you mean") || !strings.Contains(reason, want) {
		t.Errorf("reason = %q, want a hint pointing at %q", reason, want)
	}
}

// TestResolveEmptyName keeps a blank entry in skills: from producing a bogus
// candidate list.
func TestResolveEmptyName(t *testing.T) {
	res := Resolve(t.TempDir(), "")
	if res.Found() || len(res.Candidates) != 0 {
		t.Fatalf("empty name should resolve to nothing with no candidates, got %+v", res)
	}
}

// TestPrimaryPath pins the path surfaces show for a skill that is not there.
func TestPrimaryPath(t *testing.T) {
	root := t.TempDir()
	want := filepath.Join(root, ".claude", "skills", "deploying-429", "SKILL.md")
	if got := PrimaryPath(root, "deploying-429"); got != want {
		t.Errorf("PrimaryPath = %q, want %q", got, want)
	}
}
