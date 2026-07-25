//go:build !windows

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	aplog "github.com/orlandoburli/apiary/internal/log"
)

func TestCheckDotEnvPerms(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(env, []byte("KEY=val\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 0600 — owner-only: no warning expected.
	if warn := checkDotEnvPerms(env); warn != "" {
		t.Errorf("0600: unexpected warning: %q", warn)
	}

	// 0640 — group-readable: warning expected.
	if err := os.Chmod(env, 0o640); err != nil {
		t.Fatal(err)
	}
	if warn := checkDotEnvPerms(env); warn == "" {
		t.Error("0640: expected warning, got empty string")
	}

	// 0644 — world-readable: warning expected.
	if err := os.Chmod(env, 0o644); err != nil {
		t.Fatal(err)
	}
	if warn := checkDotEnvPerms(env); warn == "" {
		t.Error("0644: expected warning, got empty string")
	}

	// Non-existent path: must return empty string (no warning).
	if warn := checkDotEnvPerms(filepath.Join(dir, "missing.env")); warn != "" {
		t.Errorf("missing file: unexpected warning: %q", warn)
	}
}

// TestLoadDotEnvWarnAndLoadOnBadPerms verifies that loadDotEnv emits a warning
// but still loads credentials from a group/world-readable .env, so startup is
// never silently broken by a permission mismatch.
func TestLoadDotEnvWarnAndLoadOnBadPerms(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	// Write .env with world-readable permissions (0644 — common default).
	if err := os.WriteFile(env, []byte("APIARY_TEST_KEY_BAD=secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Unsetenv("APIARY_TEST_KEY_BAD")

	// Capture log output to confirm the warning is emitted.
	var logged []string
	aplog.SetSink(func(_, msg string) { logged = append(logged, msg) })
	defer aplog.SetSink(nil)

	fakeConfig := filepath.Join(dir, "apiary.yaml")
	loadDotEnv(fakeConfig)

	// The key must be loaded despite bad permissions.
	if got := os.Getenv("APIARY_TEST_KEY_BAD"); got != "secret" {
		t.Errorf("loadDotEnv must load credentials even from a world-readable .env; got %q, want %q", got, "secret")
	}

	// A warning must have been logged.
	found := false
	for _, msg := range logged {
		if strings.Contains(msg, "group- or world-readable") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a warning about group- or world-readable .env, but none was logged; got: %v", logged)
	}
}

// TestLoadDotEnvLoadsOnGoodPerms verifies that loadDotEnv loads credentials
// normally and emits no warning when the .env file has 0600 (owner-only) permissions.
func TestLoadDotEnvLoadsOnGoodPerms(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(env, []byte("APIARY_TEST_KEY_GOOD=value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Unsetenv("APIARY_TEST_KEY_GOOD")

	var logged []string
	aplog.SetSink(func(_, msg string) { logged = append(logged, msg) })
	defer aplog.SetSink(nil)

	fakeConfig := filepath.Join(dir, "apiary.yaml")
	loadDotEnv(fakeConfig)

	if got := os.Getenv("APIARY_TEST_KEY_GOOD"); got != "value" {
		t.Errorf("loadDotEnv must load credentials from a 0600 .env; got %q, want %q", got, "value")
	}
	if len(logged) > 0 {
		t.Errorf("expected no warnings for 0600 .env; got: %v", logged)
	}
}
