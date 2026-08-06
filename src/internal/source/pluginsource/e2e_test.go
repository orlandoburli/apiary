package pluginsource

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/plugin"
)

// TestBridgeThroughRealPluginProcess exercises the whole path — bridge →
// plugin.Client → subprocess → JSON protocol — against a shell-script plugin
// that answers source.poll with two items.
func TestBridgeThroughRealPluginProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script plugin fake requires a POSIX shell")
	}

	root := t.TempDir()
	script := `#!/bin/sh
request=$(cat)
request_id=$(printf '%s' "$request" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
printf '{"protocol":1,"request_id":"%s","result":{"items":[{"id":"e2e-1","title":"From the real process","labels":["kind:e2e"],"state":"open"},{"id":"e2e-2"}]}}\n' "$request_id"
`
	if err := os.WriteFile(filepath.Join(root, "plugin"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"schema_version": 1,
		"id":             "dev.apiary.e2e-source",
		"version":        "1.0.0",
		"apiary":         ">= 0.1.0-0",
		"protocol":       1,
		"executable":     "plugin",
		"capabilities":   []string{"source"},
	}
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(root, plugin.ManifestFilename), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	installed, err := plugin.Load(root, "dev")
	if err != nil {
		t.Fatalf("load plugin: %v", err)
	}
	client, err := plugin.NewClient(installed, plugin.InstanceConfig{ID: installed.Manifest.ID})
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	a := &Adapter{}
	a.SetID("e2e")
	a.BindPluginLookup(func(id string) (Invoker, bool) {
		if id == installed.Manifest.ID {
			return client, true
		}
		return nil, false
	})
	if err := a.Connect(context.Background(), map[string]any{"plugin": installed.Manifest.ID}); err != nil {
		t.Fatalf("connect: %v", err)
	}

	items, err := a.Poll(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(items) != 2 || items[0].ID != "e2e-1" || items[0].SourceID != "e2e" || items[1].Title != "plugin item e2e-2" {
		t.Fatalf("unexpected items: %+v", items)
	}
	if len(items[0].Labels) != 1 || items[0].Labels[0] != "kind:e2e" {
		t.Errorf("labels = %v", items[0].Labels)
	}
}
