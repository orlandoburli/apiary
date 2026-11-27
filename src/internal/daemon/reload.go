package daemon

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	aplog "github.com/orlandoburli/apiary/internal/log"
)

// defaultConfigWatchInterval is how often the config watcher stats the file.
// Tests shorten it through Dispatcher.watchInterval.
const defaultConfigWatchInterval = 2 * time.Second

// configWatchSettle is the pause between noticing a change and parsing it. An
// editor that rewrites the file in place leaves it truncated for a moment, and
// parsing then would reject a config that is about to be complete.
const configWatchSettle = 300 * time.Millisecond

// ChangeSet names the ids a reload added, removed, or changed. It deliberately
// carries ids only: a workflow's env can hold secrets, and this result is
// served by /status and printed by the CLI, so it must never echo field values.
type ChangeSet struct {
	Added   []string `json:"added,omitempty"`
	Removed []string `json:"removed,omitempty"`
	Changed []string `json:"changed,omitempty"`
}

// Empty reports whether the set names no change at all.
func (c ChangeSet) Empty() bool {
	return len(c.Added) == 0 && len(c.Removed) == 0 && len(c.Changed) == 0
}

// ReloadResult is the outcome of one ReloadConfig attempt: what was applied and
// what an operator has to do about the rest.
type ReloadResult struct {
	Applied         bool      `json:"applied"`
	ReloadedAt      time.Time `json:"reloaded_at"`
	Error           string    `json:"error,omitempty"`
	Workflows       ChangeSet `json:"workflows"`
	Agents          ChangeSet `json:"agents"`
	Sources         ChangeSet `json:"sources"`
	SettingsChanged []string  `json:"settings_changed,omitempty"`
	// RequiresRestart names the settings that were accepted into the live config
	// but are only read once, at startup, so the new value does nothing until
	// the daemon restarts. Reporting them is the difference between "reloaded"
	// and "reloaded and actually in effect".
	RequiresRestart []string `json:"requires_restart,omitempty"`
}

// startupOnlySettings names the settings the daemon reads once, at New or at
// Start, so a live swap of the config pointer cannot change them. Anything
// added here must be reflected in docs/configuration.md.
var startupOnlySettings = map[string]bool{
	"log_max_age_days":  true, // prune window captured by pruneLogsLoop at Start
	"log_max_size_mb":   true, // log rotation configured once, before the daemon runs
	"log_max_backups":   true,
	"git_hooks":         true, // hooks installed once, at Start
	"queue":             true, // store, embedded worker, and policy built at New
	"memory":            true, // store opened at New
	"events":            true, // sensitive fields set on the DB at New, retention captured at Start
	"cursor_cost":       true, // back-fill loop started at Start from the then-current settings
	"refuse_root":       true, // privilege check runs at New
	"default_fallbacks": true, // agents' fallback chains are built at New
}

// ReloadConfig re-reads the config file and, when the change can be applied
// without a restart, swaps it in.
//
// It validates through exactly the path `apiary run` uses (Load then Validate),
// and only stores the new config after every check passed: a typo must never
// leave the daemon running half a config, so the old one stays live and
// untouched on any error.
func (d *Dispatcher) ReloadConfig(ctx context.Context) ReloadResult {
	// One reload at a time: the diff is taken against the pointer being
	// replaced, and two interleaved reloads would each diff against a config
	// the other was about to discard.
	d.reloadMu.Lock()
	defer d.reloadMu.Unlock()

	res := d.reloadConfigLocked(ctx)
	stored := res
	d.lastReload = &stored
	return res
}

// reloadConfigLocked does the work of ReloadConfig. The caller holds reloadMu.
func (d *Dispatcher) reloadConfigLocked(ctx context.Context) ReloadResult {
	res := ReloadResult{
		Applied:    false,
		ReloadedAt: time.Now(),
		Workflows:  ChangeSet{},
		Agents:     ChangeSet{},
		Sources:    ChangeSet{},
	}

	cfg, err := config.Load(d.configFile)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if errs := cfg.Validate(); len(errs) > 0 {
		msgs := make([]string, 0, len(errs))
		for _, e := range errs {
			msgs = append(msgs, e.Error())
		}
		res.Error = "config validation failed: " + strings.Join(msgs, "; ")
		return res
	}

	old := d.config()
	if old == nil {
		// Only reachable for a Dispatcher built by hand (tests); an empty
		// baseline makes every entry an addition, which is accurate.
		old = &config.Config{}
	}

	res.Workflows = diffByID(old.Workflows, cfg.Workflows, func(w config.WorkflowConfig) string { return w.ID })
	res.Agents = diffByID(old.Agents, cfg.Agents, func(a config.AgentConfig) string { return a.ID })
	res.Sources = diffByID(old.Sources, cfg.Sources, func(s config.SourceConfig) string { return s.ID })
	res.SettingsChanged = diffSettings(old.Settings, cfg.Settings)

	// Sources are reconciled only after every adapter that may be needed has been
	// built. A build failure therefore rejects the reload before any loop is
	// stopped or adapter replaced: the old config and every running poll loop
	// stay exactly as they were.
	if !res.Sources.Empty() {
		if err := d.reconcileSources(ctx, cfg, res.Sources); err != nil {
			res.Error = err.Error()
			return res
		}
	}

	for _, name := range res.SettingsChanged {
		if startupOnlySettings[name] {
			res.RequiresRestart = append(res.RequiresRestart, name)
		}
	}
	// PluginDirs and Plugins are top-level, not settings, and both are consumed
	// once by plugin discovery at New.
	if !reflect.DeepEqual(old.PluginDirs, cfg.PluginDirs) {
		res.RequiresRestart = append(res.RequiresRestart, "plugin_dirs")
	}
	if !reflect.DeepEqual(old.Plugins, cfg.Plugins) {
		res.RequiresRestart = append(res.RequiresRestart, "plugins")
	}

	d.cfgPtr.Store(cfg)
	res.Applied = true
	aplog.Info("config reloaded: %s", res.Summary())
	return res
}

// reconcileSources applies a source diff to the live adapters and poll loops.
// The order is the contract behind "a rejected reload changes nothing":
//
//  1. build adapters for every added and changed source (the only step that can
//     fail), before touching any live state;
//  2. stop removed and changed loops, waiting for each goroutine to return so a
//     changed id never has two loops polling it;
//  3. install adapters and per-source stats;
//  4. start the added and changed loops, under the daemon lifetime captured at
//     Start.
//
// Only after this returns does ReloadConfig store the new config pointer.
func (d *Dispatcher) reconcileSources(ctx context.Context, cfg *config.Config, changes ChangeSet) error {
	byID := make(map[string]config.SourceConfig, len(cfg.Sources))
	for _, sc := range cfg.Sources {
		byID[sc.ID] = sc
	}

	rebuild := make(map[string]bool, len(changes.Added)+len(changes.Changed))
	for _, id := range changes.Added {
		rebuild[id] = true
	}
	for _, id := range changes.Changed {
		rebuild[id] = true
	}

	built, errs := d.buildSourceAdaptersFor(ctx, cfg, rebuild)
	if len(errs) > 0 {
		return fmt.Errorf("source adapters: %w", errs[0])
	}

	// Stop before swapping. stopSourceLoop waits for pollLoop to return, which is
	// what guarantees a changed source is never polled by both the old and the
	// new loop.
	for _, id := range changes.Removed {
		d.stopSourceLoop(id)
	}
	for _, id := range changes.Changed {
		d.stopSourceLoop(id)
	}

	d.srcMu.Lock()
	for _, id := range changes.Removed {
		delete(d.sources, id)
	}
	for id, adapter := range built {
		d.sources[id] = adapter
	}
	d.srcMu.Unlock()

	// Stats belong to the source id, not the adapter: a changed source keeps its
	// poll history, a removed one stops being reported (Status iterates the new
	// config) and its counter goes.
	d.statMu.Lock()
	for _, id := range changes.Removed {
		delete(d.stats, id)
	}
	for id := range built {
		if _, ok := d.stats[id]; !ok {
			d.stats[id] = &sourceStat{}
		}
	}
	d.statMu.Unlock()

	// Loops start last. Before Start there is no daemon lifetime to attach to:
	// the adapters are installed and Start will start every configured source,
	// including these.
	parent, running := d.sourceLoopParent()
	if !running {
		return nil
	}
	for _, id := range changes.Added {
		d.startSourceLoop(parent, byID[id], built[id])
	}
	for _, id := range changes.Changed {
		d.startSourceLoop(parent, byID[id], built[id])
	}
	return nil
}

// lastReloadResult returns a copy of the most recent reload attempt for /status,
// or nil when the daemon has not reloaded yet.
func (d *Dispatcher) lastReloadResult() *ReloadResult {
	d.reloadMu.Lock()
	defer d.reloadMu.Unlock()
	if d.lastReload == nil {
		return nil
	}
	out := *d.lastReload
	return &out
}

// Summary renders the applied diff as one log line:
// "workflows +1 ~1, agents ~1, settings log_level".
func (r ReloadResult) Summary() string {
	var parts []string
	if !r.Workflows.Empty() {
		parts = append(parts, "workflows "+r.Workflows.counts())
	}
	if !r.Agents.Empty() {
		parts = append(parts, "agents "+r.Agents.counts())
	}
	if !r.Sources.Empty() {
		parts = append(parts, "sources "+r.Sources.counts())
	}
	if len(r.SettingsChanged) > 0 {
		parts = append(parts, "settings "+strings.Join(r.SettingsChanged, ","))
	}
	if len(parts) == 0 {
		return "no changes"
	}
	return strings.Join(parts, ", ")
}

// counts renders the +added ~changed -removed shorthand used by the log line,
// omitting the parts that are zero.
func (c ChangeSet) counts() string {
	var parts []string
	if n := len(c.Added); n > 0 {
		parts = append(parts, fmt.Sprintf("+%d", n))
	}
	if n := len(c.Changed); n > 0 {
		parts = append(parts, fmt.Sprintf("~%d", n))
	}
	if n := len(c.Removed); n > 0 {
		parts = append(parts, fmt.Sprintf("-%d", n))
	}
	return strings.Join(parts, " ")
}

// diffByID compares two slices by id. reflect.DeepEqual decides "changed" but
// only the ids ever leave this function.
func diffByID[T any](old, updated []T, id func(T) string) ChangeSet {
	oldByID := make(map[string]T, len(old))
	for _, item := range old {
		oldByID[id(item)] = item
	}
	newByID := make(map[string]T, len(updated))
	for _, item := range updated {
		newByID[id(item)] = item
	}

	out := ChangeSet{}
	for key, before := range oldByID {
		after, ok := newByID[key]
		if !ok {
			out.Removed = append(out.Removed, key)
			continue
		}
		if !reflect.DeepEqual(before, after) {
			out.Changed = append(out.Changed, key)
		}
	}
	for key := range newByID {
		if _, ok := oldByID[key]; !ok {
			out.Added = append(out.Added, key)
		}
	}
	sort.Strings(out.Added)
	sort.Strings(out.Removed)
	sort.Strings(out.Changed)
	return out
}

// diffSettings names the Settings fields whose value changed, in struct order.
// Values are never included: they can hold secrets, and the field name is all
// an operator needs to know what to look at.
func diffSettings(old, updated config.Settings) []string {
	oldValue := reflect.ValueOf(old)
	newValue := reflect.ValueOf(updated)
	typ := oldValue.Type()

	var changed []string
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		if reflect.DeepEqual(oldValue.Field(i).Interface(), newValue.Field(i).Interface()) {
			continue
		}
		changed = append(changed, settingsFieldName(field))
	}
	return changed
}

// settingsFieldName is the yaml key of a Settings field, so the diff reads the
// same as the file the operator edited.
func settingsFieldName(field reflect.StructField) string {
	if tag := field.Tag.Get("yaml"); tag != "" {
		if name, _, _ := strings.Cut(tag, ","); name != "" && name != "-" {
			return name
		}
	}
	return strings.ToLower(field.Name)
}

// configFileState is the cheap fingerprint the watcher polls: mtime plus size.
type configFileState struct {
	modTime time.Time
	size    int64
}

func statConfigFile(path string) (configFileState, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return configFileState{}, false
	}
	return configFileState{modTime: info.ModTime(), size: info.Size()}, true
}

// startConfigWatcher starts the config watcher. The baseline is taken here,
// before the goroutine exists, so the config the daemon booted with is never
// re-applied during startup — and an edit made as soon as Start returns is seen.
func (d *Dispatcher) startConfigWatcher(ctx context.Context, wg *sync.WaitGroup) {
	baseline, haveBaseline := statConfigFile(d.configFile)
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.watchConfigFrom(ctx, baseline, haveBaseline)
	}()
}

// watchConfigFrom polls the config file and reloads when it changes, comparing
// each stat against the supplied baseline.
func (d *Dispatcher) watchConfigFrom(ctx context.Context, baseline configFileState, haveBaseline bool) {
	interval := d.watchInterval
	if interval <= 0 {
		interval = defaultConfigWatchInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current, ok := statConfigFile(d.configFile)
			if !ok {
				// A removed or unreadable file is not a config change; keep the
				// previous baseline so the next readable state is compared to
				// the last one actually applied.
				continue
			}
			if haveBaseline && current.modTime.Equal(baseline.modTime) && current.size == baseline.size {
				continue
			}
			baseline, haveBaseline = current, true

			// Let an in-progress write finish before reading. A writer still
			// appending past this point is caught by the next poll.
			select {
			case <-ctx.Done():
				return
			case <-time.After(configWatchSettle):
			}
			if settled, ok := statConfigFile(d.configFile); ok {
				baseline = settled
			}

			// Only a change reaches this point, so the warning is naturally once
			// per edit rather than once per poll.
			if res := d.ReloadConfig(ctx); !res.Applied {
				aplog.Warn("config watch: reload rejected, keeping the running config: %s", res.Error)
			}
		}
	}
}
