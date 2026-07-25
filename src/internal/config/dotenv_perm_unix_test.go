//go:build !windows

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckDotEnvPerms(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(env, []byte("KEY=val\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 0600 — owner-only: no error expected.
	if err := checkDotEnvPerms(env); err != nil {
		t.Errorf("0600: unexpected error: %v", err)
	}

	// 0640 — group-readable: error expected.
	if err := os.Chmod(env, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := checkDotEnvPerms(env); err == nil {
		t.Error("0640: expected error, got nil")
	}

	// 0644 — world-readable: error expected.
	if err := os.Chmod(env, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkDotEnvPerms(env); err == nil {
		t.Error("0644: expected error, got nil")
	}

	// Non-existent path: must return nil (caller handles missing file).
	if err := checkDotEnvPerms(filepath.Join(dir, "missing.env")); err != nil {
		t.Errorf("missing file: unexpected error: %v", err)
	}
}
