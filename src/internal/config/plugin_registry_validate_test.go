package config

import (
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/plugin"
	"gopkg.in/yaml.v3"
)

func TestValidateRejectsUnusableRegistries(t *testing.T) {
	cfg := &Config{Version: "1.0", PluginRegistries: []plugin.RegistrySource{
		{URL: "http://example.invalid/index.json"},
		{URL: ""},
		{URL: "https://example.invalid/index.json", PublicKey: "not-a-key"},
	}}
	var joined []string
	for _, err := range cfg.Validate() {
		joined = append(joined, err.Error())
	}
	rendered := strings.Join(joined, "\n")
	for _, want := range []string{"plugin_registries[0]", "must use https", "plugin_registries[1]", "plugin_registries[2]", "public_key"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("want %q in:\n%s", want, rendered)
		}
	}
}

// Unset means "no preference" and gets the official index; an explicitly empty
// list means the operator turned the registry off, and must stay empty.
func TestRegistriesDistinguishesUnsetFromDisabled(t *testing.T) {
	if got := (&Config{}).Registries(); len(got) != 1 || got[0].URL != plugin.DefaultRegistryURL {
		t.Fatalf("unset should default to the official index, got %v", got)
	}
	if got := (&Config{PluginRegistries: []plugin.RegistrySource{}}).Registries(); len(got) != 0 {
		t.Fatalf("an empty list disables the registry, got %v", got)
	}
}

// The scalar form predates per-registry keys and must keep working untouched.
func TestPluginRegistriesAcceptsBothYAMLForms(t *testing.T) {
	const document = `
version: "1.0"
plugin_registries:
  - https://example.invalid/index.json
  - url: file:///opt/apiary/registry/index.json
    public_key: RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(document), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.PluginRegistries) != 2 {
		t.Fatalf("want two registries, got %d", len(cfg.PluginRegistries))
	}
	if cfg.PluginRegistries[0].URL != "https://example.invalid/index.json" || cfg.PluginRegistries[0].PublicKey != "" {
		t.Fatalf("scalar form decoded wrong: %+v", cfg.PluginRegistries[0])
	}
	mirror := cfg.PluginRegistries[1]
	if mirror.URL != "file:///opt/apiary/registry/index.json" || mirror.PublicKey == "" {
		t.Fatalf("mapping form decoded wrong: %+v", mirror)
	}
	if mirror.Key() != mirror.PublicKey {
		t.Fatal("a mirror's own key must be the key it is verified against")
	}
}

// The official index is verified against the key stamped into the binary; a
// third-party registry never inherits it.
func TestKeySelection(t *testing.T) {
	previous := plugin.OfficialRegistryPublicKey
	plugin.OfficialRegistryPublicKey = "RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3"
	t.Cleanup(func() { plugin.OfficialRegistryPublicKey = previous })

	if got := (plugin.RegistrySource{URL: plugin.DefaultRegistryURL}).Key(); got != plugin.OfficialRegistryPublicKey {
		t.Fatalf("the official index must use the embedded key, got %q", got)
	}
	if got := (plugin.RegistrySource{URL: "https://mirror.invalid/index.json"}).Key(); got != "" {
		t.Fatalf("a third-party registry must not inherit the official key, got %q", got)
	}
}
