package daemon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/source"
)

// reloadNoSourcesConfig is the reloadBaseConfig with the config watcher off, so
// tests drive ReloadConfig explicitly instead of racing a background watcher.
const reloadNoSourcesConfig = `
version: "1"
settings:
  watch_config: false
runners:
  - id: claude
    type: claude
agents:
  - id: engineer
    runner: claude
    model: opus
workflows:
  - id: wf-a
    steps:
      - id: s1
        agent: engineer
        prompt: first
`

// reloadSourceConfig adds one fake source polled every 10ms.
const reloadSourceConfig = reloadNoSourcesConfig + `
sources:
  - id: src-a
    type: fake
    poll_interval: 10ms
`

// reloadSlowSourceConfig replaces src-a's interval with one hour, which makes
// "the new loop is running with the new interval" observable: pollLoop polls
// once immediately and then not again inside a test's lifetime.
const reloadSlowSourceConfig = reloadNoSourcesConfig + `
sources:
  - id: src-a
    type: fake
    poll_interval: 1h
`

// reloadFakeRegistry is the adapter factory seam for these tests. Every adapter
// it builds records polls against the registry, so a test can tell which
// instance a poll came from and how many loops were ever active at once.
type reloadFakeRegistry struct {
	mu            sync.Mutex
	byID          map[string]*reloadFakeAdapter
	unknownTypes  map[string]bool
	connectErrors map[string]error
	pollDelay     time.Duration
	inFlight      int
	maxInFlight   int
}

func newReloadFakeRegistry() *reloadFakeRegistry {
	return &reloadFakeRegistry{
		byID:          make(map[string]*reloadFakeAdapter),
		unknownTypes:  make(map[string]bool),
		connectErrors: make(map[string]error),
	}
}

func (r *reloadFakeRegistry) factory(typeName string) (source.Adapter, bool) {
	if r.unknownTypes[typeName] {
		return nil, false
	}
	adapter := &reloadFakeAdapter{registry: r, typeName: typeName, connectErr: r.connectErrors[typeName]}
	return adapter, true
}

func (r *reloadFakeRegistry) register(id string, adapter *reloadFakeAdapter) {
	r.mu.Lock()
	r.byID[id] = adapter
	r.mu.Unlock()
}

func (r *reloadFakeRegistry) adapter(id string) *reloadFakeAdapter {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byID[id]
}

func (r *reloadFakeRegistry) polls(id string) int {
	adapter := r.adapter(id)
	if adapter == nil {
		return 0
	}
	return adapter.pollCount()
}

func (r *reloadFakeRegistry) maxConcurrentPolls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.maxInFlight
}

func (r *reloadFakeRegistry) pollsInFlight() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inFlight
}

// reloadFakeAdapter is a poll-only source adapter with no items. Its only job is
// to make polling observable and, for the swap test, slow enough that an old and
// a new loop would overlap if the reload did not wait for the old one.
type reloadFakeAdapter struct {
	registry   *reloadFakeRegistry
	id         string
	typeName   string
	connectErr error

	mu    sync.Mutex
	polls int
}

func (a *reloadFakeAdapter) ID() string { return a.typeName }

func (a *reloadFakeAdapter) SetID(id string) {
	a.mu.Lock()
	a.id = id
	a.mu.Unlock()
	a.registry.register(id, a)
}

func (a *reloadFakeAdapter) Connect(context.Context, map[string]any) error { return a.connectErr }

func (a *reloadFakeAdapter) Poll(context.Context, time.Time) ([]model.SourceItem, error) {
	r := a.registry
	r.mu.Lock()
	r.inFlight++
	if r.inFlight > r.maxInFlight {
		r.maxInFlight = r.inFlight
	}
	delay := r.pollDelay
	r.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}

	r.mu.Lock()
	r.inFlight--
	r.mu.Unlock()

	a.mu.Lock()
	a.polls++
	a.mu.Unlock()
	return nil, nil
}

func (a *reloadFakeAdapter) Acknowledge(context.Context, model.SourceItem, model.AckAction) error {
	return nil
}

func (a *reloadFakeAdapter) WriteResult(context.Context, model.SourceItem, model.RunResult) error {
	return nil
}

func (a *reloadFakeAdapter) pollCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.polls
}

// newSourceReloadTestDispatcher loads path, builds the configured adapters
// through the extracted builder (the same path New uses), and installs them.
func newSourceReloadTestDispatcher(t *testing.T, path string, registry *reloadFakeRegistry) *Dispatcher {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if errs := cfg.Validate(); len(errs) > 0 {
		t.Fatalf("config does not validate: %v", errs)
	}
	d := &Dispatcher{
		configFile:    path,
		sources:       make(map[string]source.Adapter),
		sourceLoops:   make(map[string]*sourceLoop),
		stats:         make(map[string]*sourceStat),
		sourceFactory: registry.factory,
	}
	d.cfgPtr.Store(cfg)

	adapters, errs := d.buildSourceAdapters(context.Background(), cfg)
	if len(errs) > 0 {
		t.Fatalf("build adapters: %v", errs[0])
	}
	for id, adapter := range adapters {
		d.sources[id] = adapter
		d.stats[id] = &sourceStat{}
	}
	return d
}

// startReloadTestDispatcher runs Start and returns a stop function that cancels
// the daemon lifetime and waits for every loop it started.
func startReloadTestDispatcher(t *testing.T, d *Dispatcher) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	d.Start(ctx, &wg)
	return func() {
		cancel()
		wg.Wait()
	}
}

func TestReloadConfigAddsSourceAndStartsPolling(t *testing.T) {
	path := t.TempDir() + "/apiary.yaml"
	writeReloadConfig(t, path, reloadNoSourcesConfig)
	registry := newReloadFakeRegistry()
	d := newSourceReloadTestDispatcher(t, path, registry)
	stop := startReloadTestDispatcher(t, d)
	defer stop()

	writeReloadConfig(t, path, reloadSourceConfig)
	res := d.ReloadConfig(context.Background())
	if !res.Applied {
		t.Fatalf("adding a source was rejected: %s", res.Error)
	}
	if got := res.Sources.Added; len(got) != 1 || got[0] != "src-a" {
		t.Fatalf("sources added = %v, want [src-a]", got)
	}
	if d.source("src-a") == nil {
		t.Fatal("the added source has no live adapter")
	}
	waitFor(t, 3*time.Second, func() bool { return registry.polls("src-a") > 0 },
		"the added source was never polled")
}

func TestReloadConfigRemovesSourceAndStopsLoop(t *testing.T) {
	path := t.TempDir() + "/apiary.yaml"
	writeReloadConfig(t, path, reloadSourceConfig)
	registry := newReloadFakeRegistry()
	d := newSourceReloadTestDispatcher(t, path, registry)
	stop := startReloadTestDispatcher(t, d)
	defer stop()

	waitFor(t, 3*time.Second, func() bool { return registry.polls("src-a") > 0 },
		"the source was never polled before removal")
	old := registry.adapter("src-a")

	writeReloadConfig(t, path, reloadNoSourcesConfig)
	res := d.ReloadConfig(context.Background())
	if !res.Applied {
		t.Fatalf("removing a source was rejected: %s", res.Error)
	}
	if got := res.Sources.Removed; len(got) != 1 || got[0] != "src-a" {
		t.Fatalf("sources removed = %v, want [src-a]", got)
	}
	if d.source("src-a") != nil {
		t.Fatal("the removed source still has a live adapter")
	}

	// The loop must be gone, not merely detached: no poll may arrive from it
	// after the reload returned.
	before := old.pollCount()
	time.Sleep(100 * time.Millisecond)
	if after := old.pollCount(); after != before {
		t.Fatalf("removed source polled again after reload: %d -> %d", before, after)
	}
}

func TestReloadConfigEditsSourceIntervalAndRestartsLoop(t *testing.T) {
	path := t.TempDir() + "/apiary.yaml"
	writeReloadConfig(t, path, reloadSourceConfig)
	registry := newReloadFakeRegistry()
	d := newSourceReloadTestDispatcher(t, path, registry)
	stop := startReloadTestDispatcher(t, d)
	defer stop()

	waitFor(t, 3*time.Second, func() bool { return registry.polls("src-a") > 0 },
		"the source was never polled before the edit")
	old := registry.adapter("src-a")

	writeReloadConfig(t, path, reloadSlowSourceConfig)
	res := d.ReloadConfig(context.Background())
	if !res.Applied {
		t.Fatalf("editing a source was rejected: %s", res.Error)
	}
	if got := res.Sources.Changed; len(got) != 1 || got[0] != "src-a" {
		t.Fatalf("sources changed = %v, want [src-a]", got)
	}
	current := registry.adapter("src-a")
	if current == nil || current == old {
		t.Fatal("the changed source did not get a replacement adapter")
	}
	if d.source("src-a") != current {
		t.Fatal("the live adapter is not the one built for the edited source")
	}

	// pollLoop polls once on start. With the new 1h interval that is the only
	// poll for the rest of the test; the old 10ms loop would have polled many
	// times by now.
	waitFor(t, 3*time.Second, func() bool { return current.pollCount() > 0 },
		"the replacement loop never polled")
	time.Sleep(100 * time.Millisecond)
	if got := current.pollCount(); got != 1 {
		t.Fatalf("replacement loop polled %d times in 100ms, want 1 (old interval still in effect?)", got)
	}
	before := old.pollCount()
	time.Sleep(50 * time.Millisecond)
	if after := old.pollCount(); after != before {
		t.Fatalf("old loop polled after the edit: %d -> %d", before, after)
	}
}

func TestReloadConfigRejectsSourceWhenAdapterBuildFails(t *testing.T) {
	path := t.TempDir() + "/apiary.yaml"
	writeReloadConfig(t, path, reloadSourceConfig)
	registry := newReloadFakeRegistry()
	d := newSourceReloadTestDispatcher(t, path, registry)
	stop := startReloadTestDispatcher(t, d)
	defer stop()

	waitFor(t, 3*time.Second, func() bool { return registry.polls("src-a") > 0 },
		"the existing source was never polled")
	before := d.config()
	adapterBefore := registry.adapter("src-a")

	registry.mu.Lock()
	registry.connectErrors["broken"] = errors.New("token expired")
	registry.mu.Unlock()

	writeReloadConfig(t, path, reloadSourceConfig+`
  - id: src-b
    type: broken
    poll_interval: 10ms
`)
	res := d.ReloadConfig(context.Background())
	if res.Applied {
		t.Fatal("a source whose adapter cannot be built must reject the reload")
	}
	if res.Error == "" {
		t.Fatal("the rejection must carry the build error")
	}
	if d.config() != before {
		t.Fatal("a rejected reload must leave the old config live")
	}
	if d.source("src-b") != nil {
		t.Fatal("a half-built adapter was installed by a rejected reload")
	}
	if registry.adapter("src-a") != adapterBefore {
		t.Fatal("a rejected reload replaced an unrelated source's adapter")
	}

	// The old loop kept running while the rejected reload was processed.
	beforePolls := adapterBefore.pollCount()
	waitFor(t, 3*time.Second, func() bool { return adapterBefore.pollCount() > beforePolls },
		"the existing loop stopped even though the reload was rejected")
}

func TestReloadConfigChangedSourceNeverDoublePolls(t *testing.T) {
	path := t.TempDir() + "/apiary.yaml"
	writeReloadConfig(t, path, reloadSourceConfig)
	registry := newReloadFakeRegistry()
	registry.pollDelay = 25 * time.Millisecond
	d := newSourceReloadTestDispatcher(t, path, registry)
	stop := startReloadTestDispatcher(t, d)
	defer stop()

	// Reload while a poll is in flight: without waiting for the old loop to
	// return, the new loop's immediate poll would overlap it.
	waitFor(t, 3*time.Second, func() bool { return registry.pollsInFlight() > 0 },
		"no poll was ever in flight")
	writeReloadConfig(t, path, reloadSlowSourceConfig)
	res := d.ReloadConfig(context.Background())
	if !res.Applied {
		t.Fatalf("editing a source was rejected: %s", res.Error)
	}

	waitFor(t, 3*time.Second, func() bool { return registry.polls("src-a") > 0 },
		"the replacement loop never polled after the swap")
	if got := registry.maxConcurrentPolls(); got != 1 {
		t.Fatalf("polls overlapped during the source swap: max in flight = %d, want 1", got)
	}
}
