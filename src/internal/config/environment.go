package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// EnvironmentOverlay defines per-environment overrides applied on top of the
// base configuration. Only fields that are explicitly set take effect — zero
// values mean "inherit from base".
//
// Overlay precedence (highest to lowest):
//  1. Environment overlay (this struct)
//  2. Base apiary.yaml values
//
// Secret values must never appear literally; use ${VAR} env references that
// are resolved after the overlay is applied.
type EnvironmentOverlay struct {
	// Sources overrides per-source configuration, matched by id.
	// Each entry merges key-by-key into the base source's config map; keys
	// present in the overlay replace base values, absent keys are inherited.
	Sources []SourceOverlay `yaml:"sources,omitempty"`

	// Agents overrides per-agent fields, matched by id.
	Agents []AgentOverlay `yaml:"agents,omitempty"`

	// Runners overrides per-runner configuration, matched by id.
	Runners []RunnerOverlay `yaml:"runners,omitempty"`

	// Settings partially overrides global settings; only non-zero fields apply.
	Settings *EnvironmentSettingsOverlay `yaml:"settings,omitempty"`

	// EnabledSources, when non-empty, restricts the active source set to these
	// source ids. Sources not in the list are removed from the resolved config.
	EnabledSources []string `yaml:"enabled_sources,omitempty"`

	// Rollout controls gradual promotion: dispatch is restricted to tasks
	// matching all non-empty criteria.
	Rollout *RolloutPolicy `yaml:"rollout,omitempty"`
}

// SourceOverlay is a per-source patch applied by environment name.
type SourceOverlay struct {
	// ID matches the source's id field in the base config.
	ID string `yaml:"id"`
	// Config entries are merged into the base source's config map.
	Config map[string]any `yaml:"config,omitempty"`
	// PollInterval overrides the source's poll_interval.
	PollInterval string `yaml:"poll_interval,omitempty"`
}

// AgentOverlay is a per-agent patch applied by environment name.
type AgentOverlay struct {
	// ID matches the agent's id field in the base config.
	ID string `yaml:"id"`
	// Model overrides the agent's model when non-empty.
	Model string `yaml:"model,omitempty"`
	// Runner overrides the agent's runner when non-empty.
	Runner string `yaml:"runner,omitempty"`
	// Env entries are merged into the agent's env map (overlay wins on collision).
	Env map[string]string `yaml:"env,omitempty"`
	// MaxWorkers overrides the agent's max_workers when > 0.
	MaxWorkers int `yaml:"max_workers,omitempty"`
}

// RunnerOverlay is a per-runner patch applied by environment name.
type RunnerOverlay struct {
	// ID matches the runner's id field in the base config.
	ID string `yaml:"id"`
	// Config entries are merged into the base runner's config map.
	Config map[string]any `yaml:"config,omitempty"`
}

// EnvironmentSettingsOverlay partially overrides the global Settings block.
// Only non-zero fields are applied.
type EnvironmentSettingsOverlay struct {
	Concurrency int    `yaml:"concurrency,omitempty"`
	LogLevel    string `yaml:"log_level,omitempty"`
	MaxAttempts int    `yaml:"max_attempts,omitempty"`
}

// RolloutPolicy restricts which tasks receive environment-specific dispatch.
// All non-empty criteria must be satisfied (AND semantics).
type RolloutPolicy struct {
	// Sources restricts dispatch to tasks originating from these source ids.
	Sources []string `yaml:"sources,omitempty"`
	// Labels restricts dispatch to tasks carrying at least one of these labels.
	Labels []string `yaml:"labels,omitempty"`
	// Percentage restricts dispatch to this percentage of eligible tasks
	// (1–100). 0 or 100 means all eligible tasks.
	Percentage int `yaml:"percentage,omitempty"`
}

// ForEnvironment returns a deep copy of the config with the named environment
// overlay applied. It returns an error when the environment is not declared.
func (c *Config) ForEnvironment(name string) (*Config, error) {
	overlay, ok := c.Environments[name]
	if !ok {
		return nil, fmt.Errorf("environment %q not declared in config", name)
	}

	// Deep-copy via marshal/unmarshal so mutating the result never affects c.
	data, err := yaml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("copying config: %w", err)
	}
	var resolved Config
	if err := yaml.Unmarshal(data, &resolved); err != nil {
		return nil, fmt.Errorf("copying config: %w", err)
	}
	resolved.rawContent = c.rawContent

	applyEnvironmentOverlay(&resolved, overlay)
	return &resolved, nil
}

// EnvironmentNames returns the sorted list of declared environment names.
func (c *Config) EnvironmentNames() []string {
	if len(c.Environments) == 0 {
		return nil
	}
	names := make([]string, 0, len(c.Environments))
	for n := range c.Environments {
		names = append(names, n)
	}
	return names
}

// applyEnvironmentOverlay mutates dst by applying the overlay in-place.
func applyEnvironmentOverlay(dst *Config, overlay EnvironmentOverlay) {
	// Source config merges.
	for _, so := range overlay.Sources {
		for i, s := range dst.Sources {
			if s.ID != so.ID {
				continue
			}
			if len(so.Config) > 0 {
				if dst.Sources[i].Config == nil {
					dst.Sources[i].Config = make(map[string]any)
				}
				for k, v := range so.Config {
					dst.Sources[i].Config[k] = v
				}
			}
			if so.PollInterval != "" {
				dst.Sources[i].PollInterval = so.PollInterval
			}
		}
	}

	// Agent overrides.
	for _, ao := range overlay.Agents {
		for i, a := range dst.Agents {
			if a.ID != ao.ID {
				continue
			}
			if ao.Model != "" {
				dst.Agents[i].Model = ao.Model
			}
			if ao.Runner != "" {
				dst.Agents[i].Runner = ao.Runner
			}
			if ao.MaxWorkers > 0 {
				dst.Agents[i].MaxWorkers = ao.MaxWorkers
			}
			if len(ao.Env) > 0 {
				if dst.Agents[i].Env == nil {
					dst.Agents[i].Env = make(map[string]string)
				}
				for k, v := range ao.Env {
					dst.Agents[i].Env[k] = v
				}
			}
		}
	}

	// Runner config merges.
	for _, ro := range overlay.Runners {
		for i, r := range dst.Runners {
			if r.ID != ro.ID {
				continue
			}
			if len(ro.Config) > 0 {
				if dst.Runners[i].Config == nil {
					dst.Runners[i].Config = make(map[string]any)
				}
				for k, v := range ro.Config {
					dst.Runners[i].Config[k] = v
				}
			}
		}
	}

	// Settings partial override.
	if s := overlay.Settings; s != nil {
		if s.Concurrency > 0 {
			dst.Settings.Concurrency = s.Concurrency
		}
		if s.LogLevel != "" {
			dst.Settings.LogLevel = s.LogLevel
		}
		if s.MaxAttempts > 0 {
			dst.Settings.MaxAttempts = s.MaxAttempts
		}
	}

	// Filter to enabled sources.
	if len(overlay.EnabledSources) > 0 {
		allowed := make(map[string]bool, len(overlay.EnabledSources))
		for _, id := range overlay.EnabledSources {
			allowed[id] = true
		}
		kept := dst.Sources[:0]
		for _, s := range dst.Sources {
			if allowed[s.ID] {
				kept = append(kept, s)
			}
		}
		dst.Sources = kept
	}
}

// validateEnvironments checks each environment overlay for consistency.
func (c *Config) validateEnvironments() []error {
	var errs []error

	sourceIDs := make(map[string]bool, len(c.Sources))
	for _, s := range c.Sources {
		sourceIDs[s.ID] = true
	}
	agentIDs := make(map[string]bool, len(c.Agents))
	for _, a := range c.Agents {
		agentIDs[a.ID] = true
	}
	runnerIDs := make(map[string]bool, len(c.Runners))
	for _, r := range c.Runners {
		runnerIDs[r.ID] = true
	}

	for envName, overlay := range c.Environments {
		scope := fmt.Sprintf("environments.%s", envName)

		for i, so := range overlay.Sources {
			if so.ID == "" {
				errs = append(errs, fmt.Errorf("%s.sources[%d]: id is required", scope, i))
				continue
			}
			if !sourceIDs[so.ID] {
				errs = append(errs, fmt.Errorf("%s.sources[%d]: source %q not found in base config", scope, i, so.ID))
			}
			if so.PollInterval != "" {
				if _, perr := (&SourceConfig{PollInterval: so.PollInterval}).ParsedPollInterval(); perr != nil {
					errs = append(errs, fmt.Errorf("%s.sources[%d] %q: poll_interval %q: %w", scope, i, so.ID, so.PollInterval, perr))
				}
			}
		}

		for i, ao := range overlay.Agents {
			if ao.ID == "" {
				errs = append(errs, fmt.Errorf("%s.agents[%d]: id is required", scope, i))
				continue
			}
			if !agentIDs[ao.ID] {
				errs = append(errs, fmt.Errorf("%s.agents[%d]: agent %q not found in base config", scope, i, ao.ID))
			}
			if ao.Runner != "" && !runnerIDs[ao.Runner] {
				errs = append(errs, fmt.Errorf("%s.agents[%d] %q: runner %q not defined", scope, i, ao.ID, ao.Runner))
			}
		}

		for i, ro := range overlay.Runners {
			if ro.ID == "" {
				errs = append(errs, fmt.Errorf("%s.runners[%d]: id is required", scope, i))
				continue
			}
			if !runnerIDs[ro.ID] {
				errs = append(errs, fmt.Errorf("%s.runners[%d]: runner %q not found in base config", scope, i, ro.ID))
			}
		}

		for i, id := range overlay.EnabledSources {
			if id == "" {
				errs = append(errs, fmt.Errorf("%s.enabled_sources[%d]: id must not be empty", scope, i))
				continue
			}
			if !sourceIDs[id] {
				errs = append(errs, fmt.Errorf("%s.enabled_sources[%d]: source %q not found in base config", scope, i, id))
			}
		}

		if r := overlay.Rollout; r != nil {
			for i, id := range r.Sources {
				if id == "" {
					errs = append(errs, fmt.Errorf("%s.rollout.sources[%d]: id must not be empty", scope, i))
				}
			}
			if r.Percentage < 0 || r.Percentage > 100 {
				errs = append(errs, fmt.Errorf("%s.rollout.percentage must be between 0 and 100", scope))
			}
		}
	}

	return errs
}
