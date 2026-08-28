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

// writeTestIndex publishes a two-plugin index to a file:// registry.
func writeTestIndex(t *testing.T) string {
	t.Helper()
	artifact := plugin.Artifact{
		OS: runtime.GOOS, Arch: runtime.GOARCH, URL: "https://example.invalid/p.tar.gz",
		ArchiveSHA256:    "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		ExecutableSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	index := plugin.Index{SchemaVersion: 1, Plugins: []plugin.RegistryPlugin{
		{
			ID: "dev.apiary.routines", Summary: "scheduled routines",
			Capabilities: []plugin.Capability{plugin.CapabilitySource},
			Repository:   "https://example.invalid/routines",
			Releases: []plugin.Release{{Version: "1.0.0", Apiary: ">= 0.1.0-0", Protocol: plugin.ProtocolVersion,
				Conformance: &plugin.Conformance{Status: "passed", Kit: "sdk/conformance"},
				Artifacts:   []plugin.Artifact{artifact}}},
		},
		{
			ID: "dev.apiary.exporter", Summary: "event exporter",
			Capabilities: []plugin.Capability{plugin.CapabilityEventExporter},
			Repository:   "https://example.invalid/exporter",
			Releases: []plugin.Release{{Version: "2.0.0", Apiary: ">= 0.1.0-0", Protocol: plugin.ProtocolVersion,
				Artifacts: []plugin.Artifact{artifact}}},
		},
	}}
	encoded, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "index.json")
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	return "file://" + path
}

func TestPluginsSearchListsAndFilters(t *testing.T) {
	registry := writeTestIndex(t)

	cmd := newPluginsSearchCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--registry", registry})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "dev.apiary.routines") || !strings.Contains(out.String(), "dev.apiary.exporter") {
		t.Fatalf("both plugins should be listed:\n%s", out.String())
	}

	cmd = newPluginsSearchCmd()
	out.Reset()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--registry", registry, "--capability", "event_exporter"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "dev.apiary.routines") || !strings.Contains(out.String(), "dev.apiary.exporter") {
		t.Fatalf("capability filter failed:\n%s", out.String())
	}
}

func TestPluginsSearchRejectsUnknownCapability(t *testing.T) {
	cmd := newPluginsSearchCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--registry", writeTestIndex(t), "--capability", "nonsense"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "unknown capability") {
		t.Fatalf("want an unknown-capability error, got %v", err)
	}
}

func TestPluginsInfoShowsProvenanceAndTheTrustCaveat(t *testing.T) {
	cmd := newPluginsInfoCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"dev.apiary.routines", "--registry", writeTestIndex(t)})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	rendered := out.String()
	for _, want := range []string{
		"dev.apiary.routines",
		"https://example.invalid/routines", // where to read the code
		"conformance passed",               // CI's verdict, not the publisher's claim
		"not an endorsement",               // the caveat is never omitted
		"daemon's OS permissions",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("info output is missing %q:\n%s", want, rendered)
		}
	}
}

func TestPluginsInfoSuggestsOnTypo(t *testing.T) {
	cmd := newPluginsInfoCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"dev.apiary.routine", "--registry", writeTestIndex(t)})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "did you mean: dev.apiary.routines") {
		t.Fatalf("want a suggestion, got %v", err)
	}
}

func TestPluginsInfoRejectsPlaintextRegistry(t *testing.T) {
	cmd := newPluginsInfoCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"dev.apiary.routines", "--registry", "http://example.invalid/index.json"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("a plaintext registry must be refused, got %v", err)
	}
}

// An explicitly empty plugin_registries turns the registry off; commands must
// say so rather than quietly falling back to the official index.
func TestRegistryDisabledByEmptyConfigList(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apiary.yaml")
	if err := os.WriteFile(configPath, []byte("version: \"1.0\"\nplugin_registries: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	previous := configFile
	configFile = configPath
	t.Cleanup(func() { configFile = previous })

	cmd := newPluginsSearchCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "registry is disabled") {
		t.Fatalf("want a disabled-registry error, got %v", err)
	}
}

func TestSplitVersionSuffix(t *testing.T) {
	if id, version := splitVersionSuffix("dev.apiary.routines@1.2.3"); id != "dev.apiary.routines" || version != "1.2.3" {
		t.Fatalf("got %q %q", id, version)
	}
	if id, version := splitVersionSuffix("dev.apiary.routines"); id != "dev.apiary.routines" || version != "" {
		t.Fatalf("got %q %q", id, version)
	}
}
