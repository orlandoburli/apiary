package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenOutputCommitsAtomicallyAndCleansUpOnFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "usage.csv")
	if err := os.WriteFile(target, []byte("previous complete export\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Failure path: the target must still hold the previous complete file and
	// no temp file may be left behind.
	sink, _, err := openOutput(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Write([]byte("partial")); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "previous complete export\n" {
		t.Errorf("target overwritten by an uncommitted export: %q", got)
	}
	assertOnlyTarget(t, dir)

	// Success path: commit renames over the target.
	sink, commit, err := openOutput(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Write([]byte("new export\n")); err != nil {
		t.Fatal(err)
	}
	if err := commit(); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Fatalf("close after commit: %v", err)
	}
	got, _ = os.ReadFile(target)
	if string(got) != "new export\n" {
		t.Errorf("target = %q", got)
	}
	assertOnlyTarget(t, dir)
}

func assertOnlyTarget(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "usage.csv" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want only usage.csv", names)
	}
}
