package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/plugin"
)

func installDaemonTestPlugin(t *testing.T, parent, id, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires Unix")
	}
	root := filepath.Join(parent, strings.TrimPrefix(id, "dev.apiary."))
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "plugin.sh"), []byte("#!/bin/sh\n"+script+"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	manifest := plugin.Manifest{SchemaVersion: 1, ID: id, Version: "1.0.0", Apiary: ">= 0.0.0-0", Protocol: 1, Executable: "plugin.sh", Capabilities: []plugin.Capability{plugin.CapabilityEventExporter}}
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(root, plugin.ManifestFilename), raw, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestEventExporterReceivesPersistedRedactedEvent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	output := filepath.Join(root, "exported.jsonl")
	script := `IFS= read -r request
printf '%s\n' "$request" >> '` + output + `'
id=$(printf '%s' "$request" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
printf '{"protocol":1,"request_id":"%s","result":{"exported":true}}\n' "$id"`
	installDaemonTestPlugin(t, root, "dev.apiary.exporter", script)
	enabled := true
	cfg := config.LoadDefaults()
	cfg.PluginDirs = []string{root}
	cfg.Plugins = []plugin.InstanceConfig{{ID: "dev.apiary.exporter", Enabled: &enabled}}
	dbc, err := db.New(ctx, filepath.Join(root, "apiary.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbc.Close()
	dispatcher, err := New(ctx, cfg, filepath.Join(root, "apiary.yaml"), dbc, nil)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher.recordExecutionEvent(ctx, db.ExecutionEvent{Type: "test.export", Metadata: map[string]any{"token": "ghp_secret-value"}})
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "ghp_secret-value") || !strings.Contains(text, "[REDACTED]") {
		t.Fatalf("exported event was not redacted: %s", text)
	}
}

func TestEventExporterCrashAndTimeoutDoNotBreakDispatcher(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct{ name, script, timeout string }{
		{name: "crash", script: "echo boom >&2; exit 9", timeout: "1s"},
		{name: "timeout", script: "sleep 1", timeout: "20ms"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			id := "dev.apiary." + test.name
			installDaemonTestPlugin(t, root, id, test.script)
			cfg := config.LoadDefaults()
			cfg.PluginDirs = []string{root}
			cfg.Plugins = []plugin.InstanceConfig{{ID: id, Timeout: test.timeout}}
			dbc, err := db.New(ctx, filepath.Join(root, "apiary.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer dbc.Close()
			dispatcher, err := New(ctx, cfg, filepath.Join(root, "apiary.yaml"), dbc, nil)
			if err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			dispatcher.recordExecutionEvent(ctx, db.ExecutionEvent{Type: "test." + test.name})
			if time.Since(started) > 2*time.Second {
				t.Fatalf("dispatcher blocked for %s", time.Since(started))
			}
			events, err := dbc.ListExecutionEvents(ctx, db.ExecutionEventFilter{Type: "test." + test.name})
			if err != nil || len(events) != 1 {
				t.Fatalf("event persistence after plugin failure: events=%v err=%v", events, err)
			}
		})
	}
}
