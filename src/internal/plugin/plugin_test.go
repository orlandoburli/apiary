package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func writeTestPlugin(t *testing.T, parent, name, script string, manifest Manifest) string {
	t.Helper()
	root := filepath.Join(parent, name)
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	executable := "plugin.sh"
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires Unix")
	}
	if err := os.WriteFile(filepath.Join(root, executable), []byte("#!/bin/sh\n"+script+"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	manifest.SchemaVersion = ManifestSchemaVersion
	manifest.Protocol = ProtocolVersion
	manifest.Executable = executable
	if manifest.ID == "" {
		manifest.ID = "dev.apiary." + name
	}
	if manifest.Version == "" {
		manifest.Version = "1.0.0"
	}
	if manifest.Apiary == "" {
		manifest.Apiary = ">= 0.10.0-0"
	}
	if len(manifest.Capabilities) == 0 {
		manifest.Capabilities = []Capability{CapabilityEventExporter}
	}
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(root, ManifestFilename), raw, 0644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestDiscoverAndValidateConfiguredSchema(t *testing.T) {
	parent := t.TempDir()
	schema := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1}},"required":["path"],"additionalProperties":false}`)
	writeTestPlugin(t, parent, "exporter", `read request; id=$(printf '%s' "$request" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p'); printf '{"protocol":1,"request_id":"%s","result":{}}\n' "$id"`, Manifest{ConfigSchema: schema})
	registry, errs := Discover([]string{parent}, parent, "v0.10.0")
	if len(errs) != 0 {
		t.Fatalf("discover: %v", errs)
	}
	if _, ok := registry.Get("dev.apiary.exporter"); !ok {
		t.Fatal("plugin not discovered")
	}
	invalid := []InstanceConfig{{ID: "dev.apiary.exporter", Config: map[string]any{"extra": true}}}
	errs = ValidateConfigured(registry, invalid)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "path is required") {
		t.Fatalf("schema errors = %v", errs)
	}
	valid := []InstanceConfig{{ID: "dev.apiary.exporter", Config: map[string]any{"path": "events.jsonl"}}}
	if errs := ValidateConfigured(registry, valid); len(errs) != 0 {
		t.Fatalf("valid config: %v", errs)
	}
}

func TestManifestCompatibilityAndUnsupportedKeywordAreActionable(t *testing.T) {
	parent := t.TempDir()
	root := writeTestPlugin(t, parent, "future", "exit 0", Manifest{Apiary: ">= 99.0.0"})
	if _, err := Load(root, "v0.10.0"); err == nil || !strings.Contains(err.Error(), "install a compatible plugin release") {
		t.Fatalf("compatibility error = %v", err)
	}
	root = writeTestPlugin(t, parent, "schema", "exit 0", Manifest{ConfigSchema: json.RawMessage(`{"type":"object","oneOf":[]}`)})
	if _, err := Load(root, "v0.10.0"); err == nil || !strings.Contains(err.Error(), "unsupported keyword") {
		t.Fatalf("schema error = %v", err)
	}
}

func TestDiscoverRejectsDuplicateIDs(t *testing.T) {
	parent := t.TempDir()
	manifest := Manifest{ID: "dev.apiary.duplicate"}
	writeTestPlugin(t, parent, "one", "exit 0", manifest)
	writeTestPlugin(t, parent, "two", "exit 0", manifest)
	registry, errs := Discover([]string{parent}, parent, "v0.10.0")
	if len(registry.List()) != 1 || len(errs) != 1 || !strings.Contains(errs[0].Error(), "duplicate plugin id") {
		t.Fatalf("plugins=%d errs=%v", len(registry.List()), errs)
	}
}

func TestClientInvocationCrashTimeoutAndSecretAllowlist(t *testing.T) {
	parent := t.TempDir()
	okRoot := writeTestPlugin(t, parent, "ok", `read request; id=$(printf '%s' "$request" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p'); printf '{"protocol":1,"request_id":"%s","result":{"secret":"%s","hidden":"%s"}}\n' "$id" "$PLUGIN_TOKEN" "$HIDDEN_TOKEN"`, Manifest{Security: SecurityRequirements{SecretEnv: []string{"PLUGIN_TOKEN"}}})
	t.Setenv("PLUGIN_TOKEN", "allowed")
	t.Setenv("HIDDEN_TOKEN", "blocked")
	installed, err := Load(okRoot, "v0.10.0")
	if err != nil {
		t.Fatal(err)
	}
	client, _ := NewClient(installed, InstanceConfig{ID: installed.Manifest.ID})
	var result map[string]string
	if err := client.Invoke(context.Background(), CapabilityEventExporter, "export", map[string]any{"x": 1}, &result); err != nil {
		t.Fatal(err)
	}
	if result["secret"] != "allowed" || result["hidden"] != "" {
		t.Fatalf("result = %#v", result)
	}

	crashRoot := writeTestPlugin(t, parent, "crash", "echo boom >&2; exit 7", Manifest{})
	crashed, _ := Load(crashRoot, "v0.10.0")
	crashClient, _ := NewClient(crashed, InstanceConfig{})
	if err := crashClient.Invoke(context.Background(), CapabilityEventExporter, "export", nil, nil); err == nil || !strings.Contains(err.Error(), "crashed or exited") {
		t.Fatalf("crash error = %v", err)
	}
	secretCrashRoot := writeTestPlugin(t, parent, "secretcrash", "echo $PLUGIN_TOKEN >&2; exit 8", Manifest{Security: SecurityRequirements{SecretEnv: []string{"PLUGIN_TOKEN"}}})
	secretCrash, _ := Load(secretCrashRoot, "v0.10.0")
	secretCrashClient, _ := NewClient(secretCrash, InstanceConfig{})
	if err := secretCrashClient.Invoke(context.Background(), CapabilityEventExporter, "export", nil, nil); err == nil || strings.Contains(err.Error(), "allowed") || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("secret diagnostic error = %v", err)
	}
	trailingRoot := writeTestPlugin(t, parent, "trailing", `read request; id=$(printf '%s' "$request" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p'); printf '{"protocol":1,"request_id":"%s","result":{}}\n{}\n' "$id"`, Manifest{})
	trailing, _ := Load(trailingRoot, "v0.10.0")
	trailingClient, _ := NewClient(trailing, InstanceConfig{})
	if err := trailingClient.Invoke(context.Background(), CapabilityEventExporter, "export", nil, nil); err == nil || !strings.Contains(err.Error(), "more than one") {
		t.Fatalf("trailing response error = %v", err)
	}

	timeoutRoot := writeTestPlugin(t, parent, "timeout", "sleep 2", Manifest{})
	timed, _ := Load(timeoutRoot, "v0.10.0")
	timeoutClient, _ := NewClient(timed, InstanceConfig{Timeout: "20ms"})
	started := time.Now()
	if err := timeoutClient.Invoke(context.Background(), CapabilityEventExporter, "export", nil, nil); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout error = %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("timeout took %s", time.Since(started))
	}
}
