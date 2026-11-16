package dashboard

import (
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
)

// The Overview's "Concurrency: N workers" line used to be a hardcoded 4 that
// no code ever overwrote — it only looked right because 4 is also the config
// default, so a hive configured with any other value saw a number that
// matched neither its dispatcher nor its own agent worker pools.
//
// The number shown must be the concurrency the daemon actually runs with.
func TestOverviewConcurrencyComesFromConfig(t *testing.T) {
	a := newTestApp(100, 30)
	a.cfg = &config.Config{
		Settings: config.Settings{Concurrency: 6},
	}
	a.model.activeTab = tabIndex(a.model, "Overview")

	msg := a.fetchOverview()()
	ov, ok := msg.(overviewDataMsg)
	if !ok {
		t.Fatalf("expected overviewDataMsg, got %T", msg)
	}
	if ov.data.Concurrency != 6 {
		t.Errorf("Concurrency = %d, want 6 (the configured value)", ov.data.Concurrency)
	}
}

// A config with no explicit concurrency falls back to the library default
// rather than reporting 0 workers.
func TestOverviewConcurrencyFallsBackToDefault(t *testing.T) {
	a := newTestApp(100, 30)
	a.cfg = &config.Config{Settings: config.Settings{Concurrency: 0}}
	a.model.activeTab = tabIndex(a.model, "Overview")

	msg := a.fetchOverview()()
	ov, ok := msg.(overviewDataMsg)
	if !ok {
		t.Fatalf("expected overviewDataMsg, got %T", msg)
	}
	if ov.data.Concurrency != config.LoadDefaults().Settings.Concurrency {
		t.Errorf("Concurrency = %d, want the default %d", ov.data.Concurrency, config.LoadDefaults().Settings.Concurrency)
	}
	if ov.data.Concurrency <= 0 {
		t.Errorf("Concurrency = %d, want a positive worker count", ov.data.Concurrency)
	}
}

// With no config at all the Overview must still report a usable number.
func TestOverviewConcurrencyWithoutConfig(t *testing.T) {
	a := newTestApp(100, 30)
	a.cfg = nil
	a.model.activeTab = tabIndex(a.model, "Overview")

	msg := a.fetchOverview()()
	ov, ok := msg.(overviewDataMsg)
	if !ok {
		t.Fatalf("expected overviewDataMsg, got %T", msg)
	}
	if ov.data.Concurrency <= 0 {
		t.Errorf("Concurrency = %d, want a positive worker count", ov.data.Concurrency)
	}
}
