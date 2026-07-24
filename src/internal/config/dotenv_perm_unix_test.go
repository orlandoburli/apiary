//go:build !windows

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWarnDotEnvPerms(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(env, []byte("KEY=val\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 0600 — owner-only: no warning expected (does not panic).
	warnDotEnvPerms(env)

	// 0644 — world-readable: warning expected (does not panic).
	if err := os.Chmod(env, 0o644); err != nil {
		t.Fatal(err)
	}
	warnDotEnvPerms(env)

	// Non-existent path: must not panic.
	warnDotEnvPerms(filepath.Join(dir, "missing.env"))
}
