package config_test

import (
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/runner"
	_ "github.com/orlandoburli/apiary/internal/runner/providers"
)

// withKnownAdapters points config.KnownAdapters at the real runner registry
// for the duration of the test, mirroring the wiring done by the cli package.
func withKnownAdapters(t *testing.T) {
	t.Helper()
	prev := config.KnownAdapters
	config.KnownAdapters = runner.Registered
	t.Cleanup(func() { config.KnownAdapters = prev })
}

func runnerConfigFor(adapter string) config.RunnerConfig {
	rc := config.RunnerConfig{ID: "r-1"}
	if p, ok := strings.CutSuffix(adapter, "-cli"); ok && p != "" {
		rc.Type = "cli"
		rc.Provider = p
	} else {
		rc.Type = adapter
	}
	return rc
}

func TestValidate_RunnerAdapter_RegisteredCombosPass(t *testing.T) {
	withKnownAdapters(t)

	adapters := runner.Registered()
	if len(adapters) == 0 {
		t.Fatal("no adapters registered — providers import missing?")
	}
	for _, adapter := range adapters {
		t.Run(adapter, func(t *testing.T) {
			rc := runnerConfigFor(adapter)
			if got := rc.AdapterName(); got != adapter {
				t.Fatalf("test setup: AdapterName() = %q, want %q", got, adapter)
			}
			cfg := &config.Config{
				Version: "1",
				Runners: []config.RunnerConfig{rc},
			}
			if errs := cfg.Validate(); len(errs) != 0 {
				t.Errorf("expected no errors for adapter %q, got: %v", adapter, errs)
			}
		})
	}
}

func TestValidate_RunnerAdapter_UnknownProvider(t *testing.T) {
	withKnownAdapters(t)

	cfg := &config.Config{
		Version: "1",
		Runners: []config.RunnerConfig{
			{ID: "r-1", Type: "cli", Provider: "anthropic"},
		},
	}
	errs := cfg.Validate()
	if len(errs) != 1 {
		t.Fatalf("expected exactly 1 error, got %d: %v", len(errs), errs)
	}
	msg := errs[0].Error()
	for _, want := range []string{`"anthropic-cli"`, "valid combinations", "provider: claude", "type: opencode-api"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q: %s", want, msg)
		}
	}
}

func TestValidate_RunnerAdapter_BareCliType(t *testing.T) {
	withKnownAdapters(t)

	cfg := &config.Config{
		Version: "1",
		Runners: []config.RunnerConfig{
			{ID: "r-1", Type: "cli"},
		},
	}
	errs := cfg.Validate()
	if len(errs) != 1 {
		t.Fatalf("expected exactly 1 error, got %d: %v", len(errs), errs)
	}
	msg := errs[0].Error()
	if !strings.Contains(msg, `type "cli"`) || !strings.Contains(msg, "valid combinations") {
		t.Errorf("unexpected error message: %s", msg)
	}
	if strings.Contains(msg, "provider") && strings.Contains(msg, `provider ""`) {
		t.Errorf("error message should omit the empty provider: %s", msg)
	}
}

func TestValidate_RunnerAdapter_SkippedWithoutHook(t *testing.T) {
	prev := config.KnownAdapters
	config.KnownAdapters = nil
	t.Cleanup(func() { config.KnownAdapters = prev })

	cfg := &config.Config{
		Version: "1",
		Runners: []config.RunnerConfig{
			{ID: "r-1", Type: "cli", Provider: "anthropic"},
		},
	}
	if errs := cfg.Validate(); len(errs) != 0 {
		t.Errorf("expected adapter check to be skipped without hook, got: %v", errs)
	}
}
