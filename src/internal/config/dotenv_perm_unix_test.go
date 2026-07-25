//go:build !windows

package config

import (
	"os"
	"path/filepath"
	"strings"
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

// TestLoadDotEnvRefuseOnBadPerms verifies that loadDotEnv refuses to load a
// group/world-readable .env: the error is printed to stderr and the key is NOT
// set in the environment.
func TestLoadDotEnvRefuseOnBadPerms(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	// Write .env with world-readable permissions (0644 — common default).
	if err := os.WriteFile(env, []byte("APIARY_TEST_KEY_BAD=secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Ensure the key is absent before the test.
	os.Unsetenv("APIARY_TEST_KEY_BAD")

	// Capture stderr to confirm the error is emitted.
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	// loadDotEnv expects the config file path, not the .env path directly.
	fakeConfig := filepath.Join(dir, "apiary.yaml")
	loadDotEnv(fakeConfig)

	w.Close()
	os.Stderr = old

	var buf strings.Builder
	b := make([]byte, 4096)
	for {
		n, _ := r.Read(b)
		if n == 0 {
			break
		}
		buf.Write(b[:n])
	}

	if !strings.Contains(buf.String(), "group- or world-readable") {
		t.Errorf("expected error on stderr, got: %q", buf.String())
	}
	if got := os.Getenv("APIARY_TEST_KEY_BAD"); got != "" {
		t.Errorf("loadDotEnv must refuse to load credentials from a world-readable .env; got %q, want empty", got)
	}
}

// TestLoadDotEnvLoadsOnGoodPerms verifies that loadDotEnv loads credentials
// normally when the .env file has 0600 (owner-only) permissions.
func TestLoadDotEnvLoadsOnGoodPerms(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(env, []byte("APIARY_TEST_KEY_GOOD=value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Unsetenv("APIARY_TEST_KEY_GOOD")

	fakeConfig := filepath.Join(dir, "apiary.yaml")
	loadDotEnv(fakeConfig)

	if got := os.Getenv("APIARY_TEST_KEY_GOOD"); got != "value" {
		t.Errorf("loadDotEnv must load credentials from a 0600 .env; got %q, want %q", got, "value")
	}
}
