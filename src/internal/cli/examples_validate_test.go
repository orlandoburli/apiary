package cli

import (
	"path/filepath"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"

	// Register every source adapter cmd/apiary registers, so the capability
	// probes wired in init() see the real adapters the examples reference.
	_ "github.com/orlandoburli/apiary/internal/source/dynatrace"
	_ "github.com/orlandoburli/apiary/internal/source/github"
	_ "github.com/orlandoburli/apiary/internal/source/jira"
	_ "github.com/orlandoburli/apiary/internal/source/plane"
	_ "github.com/orlandoburli/apiary/internal/source/pluginsource"
	_ "github.com/orlandoburli/apiary/internal/source/prometheus"
)

// TestExampleConfigsValidate loads every shipped .apiary/example-*.yaml through
// the same path `apiary validate` uses (config.Load + Config.Validate, with the
// cli init() hooks wired) and requires it to be error-free. These files are the
// reference users copy from — docs/SCHEMA_SETUP.md presents them as such — so an
// example that no longer validates teaches the wrong config and undermines
// `apiary validate` itself. This catches the drift a new lint would otherwise
// introduce silently (e.g. the #357 source-capability check, which left
// example-apiary-full.yaml pinning a `materialize: sub_issue` step to a plane
// source that cannot host it).
//
// Plugin validation (configuredPlugins) is deliberately not run: it discovers
// binaries on disk and is environment-dependent, not a property of the file.
func TestExampleConfigsValidate(t *testing.T) {
	// Run from the repo root: the examples carry root-relative paths
	// (soul_file: .apiary/souls/…) that only resolve where the daemon runs.
	t.Chdir(filepath.Join("..", "..", ".."))

	paths, err := filepath.Glob(filepath.Join(".apiary", "example-*.yaml"))
	if err != nil {
		t.Fatalf("glob example configs: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no .apiary/example-*.yaml found — has the examples directory moved?")
	}

	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatalf("config.Load(%s): %v", path, err)
			}
			for _, e := range cfg.Validate() {
				t.Errorf("validation error: %v", e)
			}
		})
	}
}
