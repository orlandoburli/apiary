//go:build !windows

package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverSkipsGroupAndWorldWritableDirs(t *testing.T) {
	t.Run("writable parent dir is refused entirely", func(t *testing.T) {
		parent := t.TempDir()

		// Write a valid plugin so we can confirm it is NOT loaded.
		writeTestPlugin(t, parent, "legit", "exit 0", Manifest{})

		// Make the plugin directory group-writable — discovery must refuse it.
		if err := os.Chmod(parent, 0o775); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

		registry, errs := Discover([]string{parent}, parent, "v0.10.0")
		if len(registry.List()) != 0 {
			t.Fatalf("expected 0 plugins loaded from writable dir, got %d", len(registry.List()))
		}
		if len(errs) != 1 || !strings.Contains(errs[0].Error(), "group- or world-writable") {
			t.Fatalf("expected permission warning, got errs=%v", errs)
		}

		// A second path with correct permissions should still load fine.
		safeParent := t.TempDir() // TempDir creates 0700 — passes the check
		writeTestPlugin(t, safeParent, "safe", "exit 0", Manifest{})
		_ = os.WriteFile(filepath.Join(safeParent, "safe", "plugin.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o755)

		registry2, errs2 := Discover([]string{parent, safeParent}, safeParent, "v0.10.0")
		if len(errs2) != 1 || !strings.Contains(errs2[0].Error(), "group- or world-writable") {
			t.Fatalf("expected exactly 1 warning for the bad path, got errs=%v", errs2)
		}
		if _, ok := registry2.Get("dev.apiary.safe"); !ok {
			t.Fatal("safe plugin from good path should still load")
		}
	})

	t.Run("writable child plugin dir is skipped, sibling is loaded", func(t *testing.T) {
		// Parent is owner-only (0700) — passes the top-level check.
		parent := t.TempDir()

		writeTestPlugin(t, parent, "good", "exit 0", Manifest{})
		writeTestPlugin(t, parent, "bad", "exit 0", Manifest{})

		// Make the "bad" plugin subdirectory group-writable.
		badDir := filepath.Join(parent, "bad")
		if err := os.Chmod(badDir, 0o775); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(badDir, 0o755) })

		registry, errs := Discover([]string{parent}, parent, "v0.10.0")

		// The writable child must produce exactly one permission warning.
		if len(errs) != 1 || !strings.Contains(errs[0].Error(), "group- or world-writable") {
			t.Fatalf("expected 1 permission warning for writable child, got errs=%v", errs)
		}
		// The safe sibling must still be loaded.
		if _, ok := registry.Get("dev.apiary.good"); !ok {
			t.Fatal("good plugin from secure sibling dir should load")
		}
		// The writable plugin must be rejected.
		if _, ok := registry.Get("dev.apiary.bad"); ok {
			t.Fatal("bad plugin from writable dir must not load")
		}
	})
}
