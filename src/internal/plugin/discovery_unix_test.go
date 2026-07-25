//go:build !windows

package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDiscoverSkipsWritablePluginSubdir covers the multi-plugin layout where the
// top-level plugins directory is correctly owner-only but an individual plugin
// subdirectory is group- or world-writable. The writable plugin must be skipped
// while sibling plugins with correct permissions still load.
func TestDiscoverSkipsWritablePluginSubdir(t *testing.T) {
	parent := t.TempDir() // 0700 — passes the top-level check

	// A properly-permissioned sibling plugin — must still load.
	writeTestPlugin(t, parent, "clean", "exit 0", Manifest{})

	// A plugin whose own directory is group-writable — must be skipped.
	writeTestPlugin(t, parent, "dirty", "exit 0", Manifest{})
	if err := os.Chmod(filepath.Join(parent, "dirty"), 0o775); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(parent, "dirty"), 0o755) })

	registry, errs := Discover([]string{parent}, parent, "v0.10.0")

	if _, ok := registry.Get("dev.apiary.clean"); !ok {
		t.Fatal("plugin with correct permissions should load")
	}
	if _, ok := registry.Get("dev.apiary.dirty"); ok {
		t.Fatal("plugin in group-writable subdir must not load")
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "group- or world-writable") {
		t.Fatalf("expected exactly 1 permission warning for dirty subdir, got errs=%v", errs)
	}
}

func TestDiscoverSkipsGroupAndWorldWritableDirs(t *testing.T) {
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
}
