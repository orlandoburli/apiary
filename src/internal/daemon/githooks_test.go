package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
)

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v (%s)", err, out)
	}
}

func hooksPath(t *testing.T, repo string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "config", "core.hooksPath").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func TestInstallGitHooks(t *testing.T) {
	base := t.TempDir()
	hooks := filepath.Join(base, "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	// Hook script written without the executable bit: install must chmod it.
	hookFile := filepath.Join(hooks, "pre-push")
	if err := os.WriteFile(hookFile, []byte("#!/bin/sh\nexit 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	repoA := filepath.Join(base, "erp--feat")
	repoB := filepath.Join(base, "erp--fix")
	gitInit(t, repoA)
	gitInit(t, repoB)
	// A matching path that is not a git repo must be skipped without error.
	if err := os.MkdirAll(filepath.Join(base, "erp--notgit"), 0o755); err != nil {
		t.Fatal(err)
	}

	installGitHooks(config.GitHooksSettings{
		Dir:   hooks,
		Repos: []string{filepath.Join(base, "erp--*")},
	})

	for _, repo := range []string{repoA, repoB} {
		if got := hooksPath(t, repo); got != hooks {
			t.Errorf("%s: core.hooksPath = %q, want %q", repo, got, hooks)
		}
	}
	if got := hooksPath(t, filepath.Join(base, "erp--notgit")); got != "" {
		t.Errorf("non-git dir unexpectedly configured: %q", got)
	}
	fi, err := os.Stat(hookFile)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&0o111 == 0 {
		t.Errorf("hook script not made executable: mode %v", fi.Mode())
	}
}

func TestInstallGitHooksDisabledOrMissingDir(t *testing.T) {
	// Not configured: must be a no-op.
	installGitHooks(config.GitHooksSettings{})

	// Configured but dir missing: must log-and-return, not panic.
	repo := filepath.Join(t.TempDir(), "repo")
	gitInit(t, repo)
	installGitHooks(config.GitHooksSettings{
		Dir:   filepath.Join(t.TempDir(), "nope"),
		Repos: []string{repo},
	})
	if got := hooksPath(t, repo); got != "" {
		t.Errorf("hooksPath set despite missing hooks dir: %q", got)
	}
}
