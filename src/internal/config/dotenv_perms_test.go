package config

import (
	"os"
	"path/filepath"
	"testing"
)

// loadDotEnv must always load the file (warn-and-load), regardless of perms —
// the value must reach the environment even when the file is loosely permissioned.
func TestLoadDotEnv_LoadsRegardlessOfPerms(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("SEC293_TOKEN=abc123\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A group/world-readable .env (0644) must still be loaded, not refused.
	t.Setenv("SEC293_TOKEN", "")
	loadDotEnv(filepath.Join(dir, "apiary.yaml"))
	if got := os.Getenv("SEC293_TOKEN"); got != "abc123" {
		t.Fatalf("warn-and-load: expected value to be loaded even with 0644 perms, got %q", got)
	}
}

func TestLoadDotEnv_LoadsWith0600(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("SEC293_SECURE=xyz\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SEC293_SECURE", "")
	loadDotEnv(filepath.Join(dir, "apiary.yaml"))
	if got := os.Getenv("SEC293_SECURE"); got != "xyz" {
		t.Fatalf("expected 0600 .env to load, got %q", got)
	}
}
