package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/orlandoburli/apiary/internal/config"
	aplog "github.com/orlandoburli/apiary/internal/log"
)

// installGitHooks enforces settings.git_hooks on the agents' repo checkouts:
// it points core.hooksPath of every git repo matched by the configured globs
// at the shared hooks directory, and marks the hook scripts executable. This
// runs at daemon startup so a pre-push hook (running the project's local
// lint/tests) gates agent pushes mechanically — soul-file rules alone are
// advisory and agents skip them under pressure, which shows up as CI-failure
// retry loops.
//
// Best-effort by design: a repo that fails to configure is logged and skipped
// so one broken checkout cannot keep the daemon from starting.
func installGitHooks(gh config.GitHooksSettings) {
	if !gh.Enabled() {
		return
	}

	hooksDir, err := absPath(gh.Dir)
	if err != nil {
		aplog.Warn("git_hooks: resolving dir %q: %v", gh.Dir, err)
		return
	}
	info, err := os.Stat(hooksDir)
	if err != nil || !info.IsDir() {
		aplog.Warn("git_hooks: dir %q is not a directory (hooks not installed)", hooksDir)
		return
	}

	// Hook scripts must be executable or git silently ignores them.
	entries, _ := os.ReadDir(hooksDir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(hooksDir, e.Name())
		if fi, err := os.Stat(p); err == nil && fi.Mode()&0o111 == 0 {
			if err := os.Chmod(p, fi.Mode()|0o755); err != nil {
				aplog.Warn("git_hooks: chmod +x %s: %v", p, err)
			}
		}
	}

	installed := 0
	for _, pattern := range gh.Repos {
		pat, err := absPath(pattern)
		if err != nil {
			aplog.Warn("git_hooks: resolving pattern %q: %v", pattern, err)
			continue
		}
		matches, err := filepath.Glob(pat)
		if err != nil {
			aplog.Warn("git_hooks: bad glob %q: %v", pattern, err)
			continue
		}
		if len(matches) == 0 {
			aplog.Warn("git_hooks: pattern %q matched no paths", pattern)
			continue
		}
		for _, repo := range matches {
			// .git may be a directory (clone) or a file (linked worktree).
			if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
				continue
			}
			cmd := exec.Command("git", "-C", repo, "config", "core.hooksPath", hooksDir)
			if out, err := cmd.CombinedOutput(); err != nil {
				aplog.Warn("git_hooks: %s: git config core.hooksPath: %v (%s)", repo, err, strings.TrimSpace(string(out)))
				continue
			}
			installed++
		}
	}
	aplog.Info("git_hooks: core.hooksPath -> %s enforced on %d repo(s)", hooksDir, installed)
}

// absPath expands a leading ~ and makes the path absolute relative to the
// daemon working directory (the same convention soul_file paths follow).
func absPath(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expanding ~: %w", err)
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return filepath.Abs(p)
}
