package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/plugin"
)

func TestValidateCommandIncludesPluginSchema(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires Unix")
	}
	root := t.TempDir()
	pluginRoot := filepath.Join(root, "plugins", "exporter")
	if err := os.MkdirAll(pluginRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "plugin.sh"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	manifest := plugin.Manifest{SchemaVersion: 1, ID: "dev.apiary.cli-test", Version: "1.0.0", Apiary: ">= 0.0.0-0", Protocol: 1, Executable: "plugin.sh", Capabilities: []plugin.Capability{plugin.CapabilityEventExporter}, ConfigSchema: json.RawMessage(`{"type":"object","properties":{"endpoint":{"type":"string"}},"required":["endpoint"],"additionalProperties":false}`)}
	rawManifest, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(pluginRoot, plugin.ManifestFilename), rawManifest, 0644); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "apiary.yaml")
	yaml := "version: \"1.0\"\nplugin_dirs: [plugins]\nplugins:\n  - id: dev.apiary.cli-test\n    config: {}\n"
	if err := os.WriteFile(configPath, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	previous := configFile
	configFile = configPath
	t.Cleanup(func() { configFile = previous })
	cmd := newValidateCmd()
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	err := cmd.RunE(cmd, nil)
	if err == nil || !strings.Contains(stderr.String(), "$.endpoint is required") {
		t.Fatalf("err=%v stderr=%q", err, stderr.String())
	}
}
