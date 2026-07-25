package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/plugin"
)

// KnownAdapters reports the registered runner adapter names. The cli package
// injects it (config cannot import the runner package without inverting the
// dependency direction). When nil — configs built in code, isolated tests —
// the adapter check is skipped.
var KnownAdapters func() []string

// LintExpr statically parses a workflow condition expression (lowered
// condition:/fail_when:, split branch if:, parallel ${{ }} join) and returns
// a syntax error, accepting an optional ${{ }} wrapper. The cli package
// injects it (config cannot import the workflow package's parser without
// inverting the dependency direction). When nil — configs built in code,
// isolated tests — expression lint is skipped.
var LintExpr func(expr string) error

// SourceSupportsDependencyWait reports whether a source type's adapter can
// enumerate a task's upstream blockers (implements source.BlockerLister), which
// a wait_for step with kind "dependency" requires. The cli package injects it
// (config cannot import the source package without inverting the dependency
// direction). When nil — configs built in code, isolated tests — the check is
// skipped.
var SourceSupportsDependencyWait func(sourceType string) bool

// validFallbackStrategies are the accepted values for fallback_strategy fields.
var validFallbackStrategies = map[string]bool{"ordered": true, "random": true, "least_cost": true, "fastest": true}

// adapterCombos renders registered adapter names as the type/provider
// combinations users write in apiary.yaml, mirroring the table in
// docs/runners.md: "claude-cli" → `type: cli, provider: claude`; names
// without the -cli suffix are self-contained, e.g. `type: opencode-api`.
func adapterCombos(names []string) string {
	combos := make([]string, 0, len(names))
	for _, n := range names {
		if p, ok := strings.CutSuffix(n, "-cli"); ok && p != "" {
			combos = append(combos, fmt.Sprintf("`type: cli, provider: %s` (%s)", p, n))
		} else {
			combos = append(combos, fmt.Sprintf("`type: %s`", n))
		}
	}
	return strings.Join(combos, ", ")
}

// validateMCPs checks that each MCP server has a name and command, and that
// names are unique within the scope. `scope` is a human label for error
// messages (e.g. `runners[0] "claude"`).
func validateMCPs(scope string, mcps []model.MCPServer) []error {
	var errs []error
	seen := map[string]bool{}
	for j, m := range mcps {
		if m.Name == "" {
			errs = append(errs, fmt.Errorf("%s: mcps[%d]: name is required", scope, j))
		}
		if m.Command == "" {
			errs = append(errs, fmt.Errorf("%s: mcps[%d] %q: command is required", scope, j, m.Name))
		}
		if m.Name != "" && seen[m.Name] {
			errs = append(errs, fmt.Errorf("%s: mcps[%d]: duplicate name %q", scope, j, m.Name))
		}
		seen[m.Name] = true
	}
	return errs
}

// Validate checks the config for structural errors.
func (c *Config) Validate() []error {
	var errs []error
	errs = append(errs, plugin.ValidateInstanceBasics(c.Plugins)...)
	for i, path := range c.PluginDirs {
		if strings.TrimSpace(path) == "" {
			errs = append(errs, fmt.Errorf("plugin_dirs[%d]: path must not be empty", i))
		}
	}

	if c.Version == "" {
		errs = append(errs, fmt.Errorf("version is required"))
	}

	var adapters []string
	if KnownAdapters != nil {
		adapters = KnownAdapters()
	}
	adapterSet := map[string]bool{}
	for _, a := range adapters {
		adapterSet[a] = true
	}

	runnerIDs := map[string]bool{}
	for i, r := range c.Runners {
		if r.ID == "" {
			errs = append(errs, fmt.Errorf("runners[%d]: id is required", i))
		}
		if r.Type == "" {
			errs = append(errs, fmt.Errorf("runners[%d] %q: type is required", i, r.ID))
		} else if KnownAdapters != nil && !adapterSet[r.AdapterName()] {
			where := fmt.Sprintf("type %q", r.Type)
			if r.Provider != "" {
				where = fmt.Sprintf("type %q, provider %q", r.Type, r.Provider)
			}
			errs = append(errs, fmt.Errorf("runners[%d] %q: no adapter registered for %s (resolves to %q); valid combinations: %s",
				i, r.ID, where, r.AdapterName(), adapterCombos(adapters)))
		}
		if runnerIDs[r.ID] {
			errs = append(errs, fmt.Errorf("runners[%d]: duplicate id %q", i, r.ID))
		}
		runnerIDs[r.ID] = true
		errs = append(errs, validateMCPs(fmt.Sprintf("runners[%d] %q", i, r.ID), r.MCPs)...)
	}

	if c.DefaultRunner != "" && !runnerIDs[c.DefaultRunner] {
		errs = append(errs, fmt.Errorf("default_runner %q: not defined in runners", c.DefaultRunner))
	}

	sourceIDs := map[string]bool{}
	for i, s := range c.Sources {
		if s.ID == "" {
			errs = append(errs, fmt.Errorf("sources[%d]: id is required", i))
		}
		if s.Type == "" {
			errs = append(errs, fmt.Errorf("sources[%d]: type is required", i))
		}
		if sourceIDs[s.ID] {
			errs = append(errs, fmt.Errorf("sources[%d]: duplicate id %q", i, s.ID))
		}
		sourceIDs[s.ID] = true
	}

	agentIDs := map[string]bool{}
	for i, a := range c.Agents {
		if a.ID == "" {
			errs = append(errs, fmt.Errorf("agents[%d]: id is required", i))
		}
		if a.Model == "" {
			errs = append(errs, fmt.Errorf("agents[%d] %q: model is required", i, a.ID))
		}
		if a.SoulFile != "" {
			if _, err := os.Stat(a.SoulFile); err != nil {
				errs = append(errs, fmt.Errorf("agents[%d] %q: soul_file %q not found or not readable: %w", i, a.ID, a.SoulFile, err))
			}
		}
		if a.Runner != "" && !runnerIDs[a.Runner] {
			errs = append(errs, fmt.Errorf("agents[%d] %q: runner %q not defined", i, a.ID, a.Runner))
		}
		if a.FallbackStrategy != "" && !validFallbackStrategies[a.FallbackStrategy] {
			errs = append(errs, fmt.Errorf("agents[%d] %q: fallback_strategy %q: must be one of ordered, random, least_cost, fastest", i, a.ID, a.FallbackStrategy))
		}
		for j, fb := range a.Fallbacks {
			if fb.Runner == "" {
				errs = append(errs, fmt.Errorf("agents[%d] %q: fallbacks[%d]: runner is required", i, a.ID, j))
			} else if !runnerIDs[fb.Runner] {
				errs = append(errs, fmt.Errorf("agents[%d] %q: fallbacks[%d]: runner %q not defined", i, a.ID, j, fb.Runner))
			}
		}
		if agentIDs[a.ID] {
			errs = append(errs, fmt.Errorf("agents[%d]: duplicate id %q", i, a.ID))
		}
		agentIDs[a.ID] = true
		errs = append(errs, validateMCPs(fmt.Sprintf("agents[%d] %q", i, a.ID), a.MCPs)...)
	}

	workerIDs := map[string]bool{}
	for i, w := range c.Workers {
		if w.ID == "" {
			errs = append(errs, fmt.Errorf("workers[%d]: id is required", i))
		}
		if w.Runner == "" {
			errs = append(errs, fmt.Errorf("workers[%d] %q: runner is required", i, w.ID))
		}
		if w.Model == "" {
			errs = append(errs, fmt.Errorf("workers[%d] %q: model is required", i, w.ID))
		}
		if workerIDs[w.ID] {
			errs = append(errs, fmt.Errorf("workers[%d]: duplicate id %q", i, w.ID))
		}
		workerIDs[w.ID] = true
	}

	// settings.default_fallbacks: validate runner references.
	for j, fb := range c.Settings.DefaultFallbacks {
		if fb.Runner == "" {
			errs = append(errs, fmt.Errorf("settings.default_fallbacks[%d]: runner is required", j))
		} else if !runnerIDs[fb.Runner] {
			errs = append(errs, fmt.Errorf("settings.default_fallbacks[%d]: runner %q not defined", j, fb.Runner))
		}
	}

	// settings.credit_exhausted_cooldown: must be a valid duration if set.
	if c.Settings.CreditExhaustedCooldown != "" {
		if _, err := time.ParseDuration(c.Settings.CreditExhaustedCooldown); err != nil {
			errs = append(errs, fmt.Errorf("settings.credit_exhausted_cooldown %q: invalid duration: %w", c.Settings.CreditExhaustedCooldown, err))
		}
	}

	// settings.memory value checks (the block itself is optional).
	if m := c.Settings.Memory; m.TaskRetention != "" {
		if _, err := time.ParseDuration(m.TaskRetention); err != nil {
			errs = append(errs, fmt.Errorf("settings.memory.task_retention %q: invalid duration: %w", m.TaskRetention, err))
		}
	}
	if e := c.Settings.Events; e.Retention != "" {
		if _, err := time.ParseDuration(e.Retention); err != nil {
			errs = append(errs, fmt.Errorf("settings.events.retention %q: invalid duration: %w", e.Retention, err))
		}
	}
	if c.Settings.Memory.MaxInjectChars < 0 {
		errs = append(errs, fmt.Errorf("settings.memory.max_inject_chars must be >= 0"))
	}
	if c.Settings.Memory.MaxEntryBytes < 0 {
		errs = append(errs, fmt.Errorf("settings.memory.max_entry_bytes must be >= 0"))
	}
	errs = append(errs, validateQueueSettings(c.Settings.Queue)...)

	// settings.git_hooks: dir and repos only make sense together — one without
	// the other is a config mistake, not a partial setup.
	if g := c.Settings.GitHooks; g.Dir != "" || len(g.Repos) > 0 {
		if g.Dir == "" {
			errs = append(errs, fmt.Errorf("settings.git_hooks: dir is required when repos is set"))
		}
		if len(g.Repos) == 0 {
			errs = append(errs, fmt.Errorf("settings.git_hooks: repos is required when dir is set"))
		}
		for i, r := range g.Repos {
			if strings.TrimSpace(r) == "" {
				errs = append(errs, fmt.Errorf("settings.git_hooks.repos[%d]: pattern must not be empty", i))
			}
		}
	}

	// Validate profiles: referenced agent IDs and runner IDs must exist.
	for profileName, profileEntries := range c.Profiles {
		for agentID, overrides := range profileEntries {
			if _, ok := agentIDs[agentID]; !ok {
				errs = append(errs, fmt.Errorf("profiles.%s: agent %q not found in agents list", profileName, agentID))
			}
			if overrides.Runner != "" && !runnerIDs[overrides.Runner] {
				errs = append(errs, fmt.Errorf("profiles.%s.%s: runner %q not defined", profileName, agentID, overrides.Runner))
			}
			if overrides.FallbackStrategy != "" && !validFallbackStrategies[overrides.FallbackStrategy] {
				errs = append(errs, fmt.Errorf("profiles.%s.%s: fallback_strategy %q: must be one of ordered, random, least_cost, fastest", profileName, agentID, overrides.FallbackStrategy))
			}
			for j, fb := range overrides.Fallbacks {
				if fb.Runner == "" {
					errs = append(errs, fmt.Errorf("profiles.%s.%s: fallbacks[%d]: runner is required", profileName, agentID, j))
				} else if !runnerIDs[fb.Runner] {
					errs = append(errs, fmt.Errorf("profiles.%s.%s: fallbacks[%d]: runner %q not defined", profileName, agentID, j, fb.Runner))
				}
			}
		}
	}

	// Validate v2-specific authoring rules before lowering (while v2 fields are
	// still present on the raw StepConfig nodes).
	errs = append(errs, c.validateV2Workflows()...)

	// Lower v2 authored workflows to DAG IR before validation so the validator
	// sees only canonical StepConfig fields.
	if err := LowerV2WorkflowInConfig(c); err != nil {
		errs = append(errs, err)
	}
	errs = append(errs, c.validateWorkflows()...)

	// Removed-directive and unknown-field checks on the raw text (no-op when the
	// config was built in code rather than loaded from a file).
	errs = append(errs, c.lint()...)

	errs = append(errs, c.validateNotifications()...)

	return errs
}

func validateQueueSettings(settings QueueSettings) []error {
	var errs []error
	if strings.TrimSpace(settings.Listen) != "" && strings.TrimSpace(settings.WorkerToken) == "" {
		errs = append(errs, fmt.Errorf("settings.queue.worker_token is required when settings.queue.listen is configured"))
	}
	hasCert := strings.TrimSpace(settings.TLSCertFile) != ""
	hasKey := strings.TrimSpace(settings.TLSKeyFile) != ""
	if hasCert != hasKey {
		errs = append(errs, fmt.Errorf("settings.queue.tls_cert_file and settings.queue.tls_key_file must both be set or both be empty"))
	}
	if strings.TrimSpace(settings.TLSCAFile) != "" && !hasCert {
		errs = append(errs, fmt.Errorf("settings.queue.tls_ca_file requires tls_cert_file and tls_key_file to be set"))
	}
	durations := []struct {
		name, value string
		fallback    time.Duration
	}{
		{"lease_duration", settings.LeaseDuration, 30 * time.Second},
		{"heartbeat_interval", settings.HeartbeatInterval, 10 * time.Second},
		{"worker_timeout", settings.WorkerTimeout, 30 * time.Second},
		{"poll_interval", settings.PollInterval, 500 * time.Millisecond},
	}
	parsed := map[string]time.Duration{}
	for _, field := range durations {
		parsed[field.name] = field.fallback
		if field.value == "" {
			continue
		}
		duration, err := time.ParseDuration(field.value)
		if err != nil || duration <= 0 {
			errs = append(errs, fmt.Errorf("settings.queue.%s %q: must be a positive duration", field.name, field.value))
			continue
		}
		parsed[field.name] = duration
	}
	if parsed["heartbeat_interval"] >= parsed["lease_duration"] {
		errs = append(errs, fmt.Errorf("settings.queue.heartbeat_interval must be shorter than lease_duration"))
	}
	if settings.WorkerCapacity < 0 {
		errs = append(errs, fmt.Errorf("settings.queue.worker_capacity must be >= 0"))
	}
	limits := settings.Concurrency
	for name, value := range map[string]int{"default_project": limits.DefaultProject, "default_source": limits.DefaultSource, "default_agent": limits.DefaultAgent, "default_runner": limits.DefaultRunner, "default_pool": limits.DefaultPool} {
		if value < 0 {
			errs = append(errs, fmt.Errorf("settings.queue.concurrency.%s must be >= 0", name))
		}
	}
	for name, values := range map[string]map[string]int{"projects": limits.Projects, "sources": limits.Sources, "agents": limits.Agents, "runners": limits.Runners, "pools": limits.Pools} {
		for key, value := range values {
			if strings.TrimSpace(key) == "" || value <= 0 {
				errs = append(errs, fmt.Errorf("settings.queue.concurrency.%s[%q] must have a non-empty key and positive limit", name, key))
			}
		}
	}
	return errs
}

// validateNotifications checks the top-level notifications block: watching
// labels without a channel (or vice versa) is a configuration mistake that
// would silently never notify.
func (c *Config) validateNotifications() []error {
	n := c.Notifications
	if n == nil {
		return nil
	}
	var errs []error
	if len(n.OnLabels) == 0 {
		errs = append(errs, fmt.Errorf("notifications: on_labels is required (which labels should trigger a notification)"))
	}
	if len(n.Channels) == 0 {
		errs = append(errs, fmt.Errorf("notifications: at least one channel is required"))
	}
	for i, ch := range n.Channels {
		switch ch.Type {
		case "command":
			if strings.TrimSpace(ch.Run) == "" {
				errs = append(errs, fmt.Errorf("notifications.channels[%d]: run is required for type \"command\"", i))
			}
		default:
			errs = append(errs, fmt.Errorf("notifications.channels[%d]: unknown type %q (only \"command\" is supported)", i, ch.Type))
		}
	}
	return errs
}
