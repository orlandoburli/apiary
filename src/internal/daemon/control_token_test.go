package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateAndReadControlToken(t *testing.T) {
	dir := t.TempDir()

	token, err := GenerateControlToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 64 { // 32 bytes hex-encoded
		t.Fatalf("expected 64-char token, got %d chars", len(token))
	}

	// File must exist with mode 0600.
	path := ControlTokenPath(dir)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("expected mode 0600, got %o", got)
	}

	// ReadControlToken must return the same value.
	if got := ReadControlToken(dir); got != token {
		t.Fatalf("read %q, want %q", got, token)
	}

	// Re-generating must invalidate the old token.
	token2, _ := GenerateControlToken(dir)
	if token2 == token {
		t.Fatal("second generate returned same token")
	}
	if got := ReadControlToken(dir); got != token2 {
		t.Fatalf("after regen read %q, want %q", got, token2)
	}
}

func TestReadControlTokenMissingFile(t *testing.T) {
	dir := t.TempDir()
	// File doesn't exist — ReadControlToken should return "".
	if got := ReadControlToken(dir); got != "" {
		t.Fatalf("expected empty string for missing token, got %q", got)
	}
}

func TestControlTokenPath(t *testing.T) {
	got := ControlTokenPath("/some/dir")
	want := filepath.Join("/some/dir", "control.token")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
