package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
)

// --profile is not part of the file: the daemon overlays it on the loaded
// config at startup. A reload loads the file again, so without re-applying the
// overlay every save would silently drop the profile — and the watcher would do
// it on its own, at the first edit.
func TestReloadKeepsTheProfileOverrides(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "apiary.yaml")
	yaml := `version: "1"
runners:
  - id: claude
    type: claude-cli
  - id: cursor
    type: cursor-cli
agents:
  - id: engineer
    model: base/model
    runner: claude
profiles:
  prod:
    engineer:
      runner: cursor
      model: prod/model
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if errs := cfg.Validate(); len(errs) > 0 {
		t.Fatalf("validate: %v", errs)
	}
	// Startup overlay, as New does it.
	applyProfileOverrides(cfg, "prod")

	d := dispatcherWithConfig(cfg, &Dispatcher{configFile: path, profileName: "prod"})

	res := d.ReloadConfig(ctx)
	if !res.Applied {
		t.Fatalf("reload rejected: %s", res.Error)
	}
	if n := len(res.Agents.Added) + len(res.Agents.Removed) + len(res.Agents.Changed); n != 0 {
		t.Errorf("a no-op reload reported %d agent change(s): added=%v removed=%v changed=%v",
			n, res.Agents.Added, res.Agents.Removed, res.Agents.Changed)
	}
	if got := d.config().Agents[0]; got.Runner != "cursor" || got.Model != "prod/model" {
		t.Errorf("reloaded agent = runner %q model %q, want the profile's cursor/prod/model", got.Runner, got.Model)
	}
}
