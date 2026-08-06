package config

import (
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/plugin"
)

func pluginSourceConfig(pluginRef string, instances []plugin.InstanceConfig) *Config {
	cfg := map[string]any{}
	if pluginRef != "" {
		cfg["plugin"] = pluginRef
	}
	return &Config{
		Version: "1",
		Sources: []SourceConfig{{ID: "bridged", Type: "plugin", Config: cfg}},
		Plugins: instances,
	}
}

func hasErrContaining(errs []error, want string) bool {
	for _, err := range errs {
		if strings.Contains(err.Error(), want) {
			return true
		}
	}
	return false
}

func TestPluginSourceReferenceValidation(t *testing.T) {
	disabled := false
	declared := []plugin.InstanceConfig{{ID: "com.example.mon"}}

	cases := []struct {
		name      string
		cfg       *Config
		wantErr   string
		wantClean bool
	}{
		{"missing plugin key", pluginSourceConfig("", nil), "config.plugin is required", false},
		{"undeclared plugin", pluginSourceConfig("com.example.nope", declared), "not an enabled plugins[] instance", false},
		{"disabled plugin", pluginSourceConfig("com.example.off", []plugin.InstanceConfig{{ID: "com.example.off", Enabled: &disabled}}), "not an enabled plugins[] instance", false},
		{"valid reference", pluginSourceConfig("com.example.mon", declared), "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := tc.cfg.Validate()
			if tc.wantClean {
				if hasErrContaining(errs, "config.plugin") {
					t.Errorf("unexpected plugin-source error: %v", errs)
				}
				return
			}
			if !hasErrContaining(errs, tc.wantErr) {
				t.Errorf("Validate() = %v, want error containing %q", errs, tc.wantErr)
			}
		})
	}
}
