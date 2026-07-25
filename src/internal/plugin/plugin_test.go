package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
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
	execPath := filepath.Join(root, executable)
	if err := os.WriteFile(execPath, []byte("#!/bin/sh\n"+script+"\n"), 0755); err != nil {
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
	if manifest.Checksum == "" {
		manifest.Checksum = executableChecksum(t, execPath)
	}
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(root, ManifestFilename), raw, 0644); err != nil {
		t.Fatal(err)
	}
	return root
}

func executableChecksum(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
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

func TestChecksumVerification(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires Unix")
	}
	parent := t.TempDir()

	// Missing checksum is refused: write the executable manually, then write a
	// manifest with no checksum field so json.Marshal omits it.
	nocsRoot := filepath.Join(parent, "nochecksum")
	if err := os.MkdirAll(nocsRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nocsRoot, "plugin.sh"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	// Use a manifest struct with Checksum omitted — the zero value "" is serialised.
	nocsManifest := Manifest{
		SchemaVersion: ManifestSchemaVersion, Protocol: ProtocolVersion,
		Executable: "plugin.sh", ID: "dev.apiary.nochecksum", Version: "1.0.0",
		Apiary: ">= 0.10.0-0", Capabilities: []Capability{CapabilityEventExporter},
	}
	raw, _ := json.Marshal(nocsManifest)
	if err := os.WriteFile(filepath.Join(nocsRoot, ManifestFilename), raw, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(nocsRoot, "v0.10.0"); err == nil || !strings.Contains(err.Error(), "sha256:<64-hex-chars>") {
		t.Fatalf("missing checksum error = %v", err)
	}

	// Wrong checksum is refused.
	wrongRoot := writeTestPlugin(t, parent, "wrongchecksum", "exit 0", Manifest{
		Checksum: "sha256:" + strings.Repeat("0", 64),
	})
	if _, err := Load(wrongRoot, "v0.10.0"); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("wrong checksum error = %v", err)
	}

	// Correct checksum loads.
	goodRoot := writeTestPlugin(t, parent, "goodchecksum", "exit 0", Manifest{})
	if _, err := Load(goodRoot, "v0.10.0"); err != nil {
		t.Fatalf("good checksum load: %v", err)
	}

	// Tampered binary after load is caught by NewClient.
	tamperedRoot := writeTestPlugin(t, parent, "tampered", "exit 0", Manifest{})
	installed, err := Load(tamperedRoot, "v0.10.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tamperedRoot, "plugin.sh"), []byte("#!/bin/sh\necho tampered\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewClient(installed, InstanceConfig{ID: installed.Manifest.ID}); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("tampered binary error = %v", err)
	}
}

func TestSecurityPathValidation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires Unix")
	}
	parent := t.TempDir()

	// Relative read path is rejected.
	root := writeTestPlugin(t, parent, "relpath", "exit 0", Manifest{
		Security: SecurityRequirements{ReadPaths: []string{"relative/path"}},
	})
	if _, err := Load(root, "v0.10.0"); err == nil || !strings.Contains(err.Error(), "absolute path") {
		t.Fatalf("relative read_path error = %v", err)
	}

	// Unclean write path is rejected.
	root2 := writeTestPlugin(t, parent, "unclean", "exit 0", Manifest{
		Security: SecurityRequirements{WritePaths: []string{"/tmp//data"}},
	})
	if _, err := Load(root2, "v0.10.0"); err == nil || !strings.Contains(err.Error(), "clean absolute path") {
		t.Fatalf("unclean write_path error = %v", err)
	}

	// Duplicate read path is rejected.
	root3 := writeTestPlugin(t, parent, "duppath", "exit 0", Manifest{
		Security: SecurityRequirements{ReadPaths: []string{"/tmp", "/tmp"}},
	})
	if _, err := Load(root3, "v0.10.0"); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate read_path error = %v", err)
	}

	// Valid paths load successfully.
	root4 := writeTestPlugin(t, parent, "validpaths", "exit 0", Manifest{
		Security: SecurityRequirements{ReadPaths: []string{"/tmp"}, WritePaths: []string{"/tmp/out"}},
	})
	if _, err := Load(root4, "v0.10.0"); err != nil {
		t.Fatalf("valid paths load: %v", err)
	}
}

func TestPermissionEnvironmentVariables(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires Unix")
	}
	parent := t.TempDir()

	// Plugin with network=false: APIARY_PLUGIN_NETWORK must be false and
	// the plugin must NOT receive HTTP_PROXY even if set in the host env.
	netOffScript := `read req
id=$(printf '%s' "$req" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
printf '{"protocol":1,"request_id":"%s","result":{"net":"%s","proxy":"%s","read":"%s","write":"%s"}}\n' \
  "$id" "$APIARY_PLUGIN_NETWORK" "$HTTP_PROXY" "$APIARY_PLUGIN_READ_PATHS" "$APIARY_PLUGIN_WRITE_PATHS"`

	netOffRoot := writeTestPlugin(t, parent, "netoff", netOffScript, Manifest{
		Security: SecurityRequirements{Network: false, ReadPaths: []string{"/tmp"}, WritePaths: []string{"/tmp/out"}},
	})
	t.Setenv("HTTP_PROXY", "http://proxy.example.com:3128")
	installed, err := Load(netOffRoot, "v0.10.0")
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(installed, InstanceConfig{ID: installed.Manifest.ID})
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]string
	if err := client.Invoke(context.Background(), CapabilityEventExporter, "export", nil, &result); err != nil {
		t.Fatal(err)
	}
	if result["net"] != "false" {
		t.Errorf("APIARY_PLUGIN_NETWORK = %q, want \"false\"", result["net"])
	}
	if result["proxy"] != "" {
		t.Errorf("HTTP_PROXY leaked to network=false plugin: %q", result["proxy"])
	}
	if result["read"] != "/tmp" {
		t.Errorf("APIARY_PLUGIN_READ_PATHS = %q, want \"/tmp\"", result["read"])
	}
	if result["write"] != "/tmp/out" {
		t.Errorf("APIARY_PLUGIN_WRITE_PATHS = %q, want \"/tmp/out\"", result["write"])
	}

	// Plugin with network=true: APIARY_PLUGIN_NETWORK must be true and
	// HTTP_PROXY from the host must be forwarded.
	netOnScript := `read req
id=$(printf '%s' "$req" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
printf '{"protocol":1,"request_id":"%s","result":{"net":"%s","proxy":"%s"}}\n' \
  "$id" "$APIARY_PLUGIN_NETWORK" "$HTTP_PROXY"`

	netOnRoot := writeTestPlugin(t, parent, "neton", netOnScript, Manifest{
		Security: SecurityRequirements{Network: true},
	})
	installedOn, err := Load(netOnRoot, "v0.10.0")
	if err != nil {
		t.Fatal(err)
	}
	clientOn, err := NewClient(installedOn, InstanceConfig{ID: installedOn.Manifest.ID})
	if err != nil {
		t.Fatal(err)
	}
	var resultOn map[string]string
	if err := clientOn.Invoke(context.Background(), CapabilityEventExporter, "export", nil, &resultOn); err != nil {
		t.Fatal(err)
	}
	if resultOn["net"] != "true" {
		t.Errorf("APIARY_PLUGIN_NETWORK = %q, want \"true\"", resultOn["net"])
	}
	if resultOn["proxy"] != "http://proxy.example.com:3128" {
		t.Errorf("HTTP_PROXY not forwarded to network=true plugin: %q", resultOn["proxy"])
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
