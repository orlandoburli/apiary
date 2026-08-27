package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkillsConfig writes a minimal config declaring one agent with one skill
// into a fresh directory, chdirs into it (skills resolve relative to the
// working directory, like soul_file) and points the cli at it.
func writeSkillsConfig(t *testing.T, skill string) string {
	t.Helper()
	root := t.TempDir()
	yaml := "version: \"1.0\"\n" +
		"agents:\n" +
		"  - id: engineer\n" +
		"    model: claude-sonnet-5\n" +
		"    skills: [" + skill + "]\n"
	configPath := filepath.Join(root, "apiary.yaml")
	if err := os.WriteFile(configPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	previous := configFile
	configFile = configPath
	t.Cleanup(func() { configFile = previous })
	return root
}

// runValidate runs the validate command as `apiary validate` does and returns
// its error plus what it printed to stderr.
func runValidate(t *testing.T) (error, string) {
	t.Helper()
	cmd := newValidateCmd()
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	return cmd.RunE(cmd, nil), stderr.String()
}

// TestValidateFailsOnUnresolvableSkill is the headline behaviour of issue #429:
// a declared skill that is nowhere on disk must fail validation, and the message
// must list the candidate paths so the user can see the expected shape.
func TestValidateFailsOnUnresolvableSkill(t *testing.T) {
	writeSkillsConfig(t, "no-such-skill-429")

	err, stderr := runValidate(t)
	if err == nil {
		t.Fatalf("validate exited 0 on an unresolvable skill; stderr=%q", stderr)
	}
	if !strings.Contains(stderr, `skill "no-such-skill-429" not found`) {
		t.Errorf("stderr = %q, want the missing-skill error", stderr)
	}
	if !strings.Contains(stderr, filepath.Join(".apiary", "skills", "no-such-skill-429", "SKILL.md")) {
		t.Errorf("stderr = %q, want the candidate paths tried", stderr)
	}
}

// TestValidateHintsFlatSkillFile covers the mistake users actually make: the
// skill written as `<name>.md` instead of `<name>/SKILL.md`.
func TestValidateHintsFlatSkillFile(t *testing.T) {
	root := writeSkillsConfig(t, "flat-skill-429")
	flat := filepath.Join(root, ".apiary", "skills", "flat-skill-429.md")
	if err := os.MkdirAll(filepath.Dir(flat), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(flat, []byte("# flat\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err, stderr := runValidate(t)
	if err == nil {
		t.Fatalf("validate exited 0 on a flat skill file; stderr=%q", stderr)
	}
	want := filepath.Join(".apiary", "skills", "flat-skill-429", "SKILL.md")
	if !strings.Contains(stderr, "did you mean") || !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want a hint pointing at %q", stderr, want)
	}
}

// TestValidatePassesOnResolvableSkill guards the other side: a skill laid out
// correctly must not trip the new check.
func TestValidatePassesOnResolvableSkill(t *testing.T) {
	root := writeSkillsConfig(t, "good-skill-429")
	path := filepath.Join(root, ".apiary", "skills", "good-skill-429", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# good\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err, stderr := runValidate(t); err != nil {
		t.Fatalf("validate failed on a resolvable skill: %v; stderr=%q", err, stderr)
	}
}
