package config

import (
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/plugin"
)

func TestValidatePluginBasics(t *testing.T) {
	cfg := LoadDefaults()
	cfg.PluginDirs = []string{""}
	cfg.Plugins = []plugin.InstanceConfig{{ID: "dev.apiary.test", Timeout: "never"}, {ID: "dev.apiary.test"}}
	errs := cfg.Validate()
	joined := make([]string, len(errs))
	for i, err := range errs {
		joined[i] = err.Error()
	}
	message := strings.Join(joined, "\n")
	for _, want := range []string{"plugin_dirs[0]: path must not be empty", "timeout \"never\"", "duplicate id \"dev.apiary.test\""} {
		if !strings.Contains(message, want) {
			t.Fatalf("missing %q in %s", want, message)
		}
	}
}
