package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/plugin"
	"github.com/orlandoburli/apiary/internal/skills"
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

// SourceCaps reports which optional write capabilities a source type's
// adapter implements. Read-only sources (e.g. prometheus alerts) implement
// none of them; workflow features that need one are rejected at validation.
type SourceCaps struct {
	SetState     bool // source.StateSetter — on_complete/on_fail set_state
	AddLabels    bool // source.LabelAdder — add_labels, assign_from_output
	RemoveLabels bool // source.LabelRemover — remove_labels
	Approvals    bool // source.TaskPoller — approval steps
	CIWait       bool // source.CIStatusPoller — wait_for kind "ci"
	PRCIWait     bool // source.PRCIStatusPoller — wait_for kind "ci" with ci_source
	SubIssues    bool // source.SubIssueCreator — materialize: sub_issue
	Resolvable   bool // source.ItemResolver — interrupt_on_resolve
}

// SourceCapabilities reports the SourceCaps of a source type's adapter. The
// cli package injects it (config cannot import the source package without
// inverting the dependency direction). When nil — configs built in code,
// isolated tests — the capability checks are skipped, mirroring KnownAdapters.
var SourceCapabilities func(sourceType string) SourceCaps

// SourceSupportsDependencyWait reports whether a source type's adapter can
// enumerate a task's upstream blockers (implements source.BlockerLister), which
// a wait_for step with kind "dependency" requires. The cli package injects it
// (config cannot import the source package without inverting the dependency
// direction). When nil — configs built in code, isolated tests — the check is
// skipped.
var SourceSupportsDependencyWait func(sourceType string) bool

// SourceSupportsPREvents reports whether a source type's adapter can poll
// pull-request events (implements source.PREventPoller), which a workflow
// trigger with an `on:` event kind requires. The cli package injects it (config
// cannot import the source package without inverting the dependency direction).
// When nil — configs built in code, isolated tests — the check is skipped.
var SourceSupportsPREvents func(sourceType string) bool

// validAgentTools are the tool names accepted in agents[].permissions.
var validAgentTools = map[string]bool{
	"read": true, "glob": true, "grep": true, "task": true,
	"edit": true, "bash": true, "webfetch": true,
}

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

	errs = append(errs, validateImprove(c)...)

	// Enabled plugin instance ids, for plugin-bridged source references.
	enabledPlugins := map[string]bool{}
	for _, p := range c.Plugins {
		if p.IsEnabled() {
			enabledPlugins[p.ID] = true
		}
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

		// A key the adapter never reads is a silent misconfiguration: the
		// daemon starts, the source polls, and the setting simply does not
		// apply (`token:` instead of `api_key:` polls GitHub anonymously).
		// Reject it here, where it is still cheap to fix.
		handledElsewhere := map[string]bool{}
		if s.Type == "plugin" {
			// The dedicated check below reports a missing config.plugin with a
			// message that also explains what to point it at.
			handledElsewhere["plugin"] = true
		}
		errs = append(errs, validateSourceConfig(fmt.Sprintf("sources[%d] %q", i, s.ID), s.Type, s.Config, handledElsewhere)...)

		// A plugin-bridged source must name a declared, enabled plugin
		// instance — the daemon resolves it at startup, but a broken
		// reference should fail `apiary validate`, not the daemon.
		if s.Type == "plugin" {
			pluginID, _ := s.Config["plugin"].(string)
			if strings.TrimSpace(pluginID) == "" {
				errs = append(errs, fmt.Errorf("sources[%d] %q: config.plugin is required for type \"plugin\" (the id of an enabled plugins[] instance with the \"source\" capability)", i, s.ID))
			} else if !enabledPlugins[pluginID] {
				errs = append(errs, fmt.Errorf("sources[%d] %q: config.plugin %q is not an enabled plugins[] instance", i, s.ID, pluginID))
			}
		}

		// interrupt_on_resolve needs an adapter that can tell a resolved item
		// from a merely invisible one. Silently ignoring the flag on a source
		// that cannot would look like a policy that is on but never fires.
		if s.InterruptOnResolve && SourceCapabilities != nil && s.Type != "" {
			if !SourceCapabilities(s.Type).Resolvable {
				errs = append(errs, fmt.Errorf("sources[%d] %q: interrupt_on_resolve is not supported by source type %q — only monitoring sources that can report a resolved item support it", i, s.ID, s.Type))
			}
		}
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
		// A declared skill that resolves to nothing is the same class of error
		// as a missing soul_file: the agent runs without instructions it was
		// configured to have, and nothing else in a run says so.
		for _, name := range a.Skills {
			if strings.TrimSpace(name) == "" {
				errs = append(errs, fmt.Errorf("agents[%d] %q: skills: skill name must not be empty", i, a.ID))
				continue
			}
			if res := skills.Resolve("", name); !res.Found() {
				errs = append(errs, fmt.Errorf("agents[%d] %q: %s", i, a.ID, res.Reason()))
			}
		}
		if a.Runner != "" && !runnerIDs[a.Runner] {
			errs = append(errs, fmt.Errorf("agents[%d] %q: runner %q not defined", i, a.ID, a.Runner))
		}
		if a.FallbackStrategy != "" && !validFallbackStrategies[a.FallbackStrategy] {
			errs = append(errs, fmt.Errorf("agents[%d] %q: fallback_strategy %q: must be one of ordered, random, least_cost, fastest", i, a.ID, a.FallbackStrategy))
		}
		for tool, value := range a.Permissions {
			if !validAgentTools[tool] {
				errs = append(errs, fmt.Errorf("agents[%d] %q: permissions: unknown tool %q; supported: bash, edit, glob, grep, read, task, webfetch", i, a.ID, tool))
			}
			if value != "allow" && value != "deny" {
				errs = append(errs, fmt.Errorf("agents[%d] %q: permissions.%s: %q must be \"allow\" or \"deny\"", i, a.ID, tool, value))
			}
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

	// Capability lint: reject workflow features that need a write capability
	// (set_state, approvals, wait_for ci, …) against sources that lack it.
	errs = append(errs, c.validateSourceCapabilities()...)

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

// validateImprove checks settings.improve. The advisor agent must exist, and an
// effort key must be one the command actually accepts — a typo like
// `effort_models.stanadrd` would otherwise be silently ignored, leaving the
// operator believing they had pinned a cheaper model while paying for the
// agent's default.
func validateImprove(c *Config) []error {
	var errs []error
	im := c.Settings.Improve

	if im.Agent != "" {
		found := false
		for _, a := range c.Agents {
			if a.ID == im.Agent {
				found = true
				break
			}
		}
		if !found {
			errs = append(errs, fmt.Errorf("settings.improve.agent %q: not defined in agents", im.Agent))
		}
	}

	valid := map[string]bool{"quick": true, "standard": true, "deep": true}
	for effort, model := range im.EffortModels {
		if !valid[effort] {
			errs = append(errs, fmt.Errorf("settings.improve.effort_models: unknown effort %q (want quick, standard or deep)", effort))
		}
		if model == "" {
			errs = append(errs, fmt.Errorf("settings.improve.effort_models.%s: model is empty", effort))
		}
	}
	return errs
}
