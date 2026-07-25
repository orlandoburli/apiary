package config

import (
	"crypto/sha256"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// EnvironmentConfig defines one named environment (e.g. development, staging,
// production). It holds overlays applied on top of the base configuration when
// that environment is active. Secret values must not be stored here; use
// ${VAR} references that resolve from per-environment .env files instead.
type EnvironmentConfig struct {
	// Sources selectively overrides source settings by source ID. Only listed
	// sources are affected; unlisted sources are inherited unchanged.
	Sources []EnvironmentSourceOverride `yaml:"sources,omitempty"`
	// Settings overrides top-level daemon runtime settings for this environment.
	Settings *EnvironmentSettingsOverride `yaml:"settings,omitempty"`
	// Rollout restricts dispatch to a subset of eligible tasks, enabling
	// gradual rollout before promoting a workflow to full production traffic.
	Rollout *RolloutConfig `yaml:"rollout,omitempty"`
}

// EnvironmentSourceOverride selectively overrides one source's config.
// Only the keys listed in Config are merged; unmentioned config keys are kept.
type EnvironmentSourceOverride struct {
	// ID is the source id to override (required).
	ID string `yaml:"id"`
	// Enabled, when set to false, disables the source entirely for this
	// environment — it will not be polled and no tasks are dispatched from it.
	Enabled *bool `yaml:"enabled,omitempty"`
	// Config is a partial map of source config keys to override. Values support
	// ${VAR} expansion the same way the top-level config does.
	Config map[string]any `yaml:"config,omitempty"`
}

// EnvironmentSettingsOverride overrides per-environment runtime settings.
// Zero values mean "inherit from the base config".
type EnvironmentSettingsOverride struct {
	Concurrency int    `yaml:"concurrency,omitempty"`
	LogLevel    string `yaml:"log_level,omitempty"`
	MaxAttempts int    `yaml:"max_attempts,omitempty"`
}

// RolloutConfig gates dispatch to a subset of eligible tasks, enabling
// gradual rollout. All non-empty fields are ANDed: a task must satisfy every
// active filter to be dispatched. An empty RolloutConfig dispatches everything.
type RolloutConfig struct {
	// Sources restricts dispatch to tasks originating from these source IDs.
	// Empty means all sources are eligible.
	Sources []string `yaml:"sources,omitempty"`
	// Labels restricts dispatch to tasks that carry at least one of these
	// labels. Empty means any label set is eligible.
	Labels []string `yaml:"labels,omitempty"`
	// Percent restricts dispatch to this percentage (1–100) of eligible tasks.
	// The selection is deterministic: the same task ID always produces the same
	// decision across daemon restarts. 0 means no percentage filter.
	Percent int `yaml:"percent,omitempty"`
}

// ResolveForEnvironment applies the named environment's overlays to a
// deep-copy of c and returns the resolved config. The original is never
// modified. If name is empty the original is returned unchanged.
// Returns an error when name is non-empty but not defined in environments:.
func (c *Config) ResolveForEnvironment(name string) (*Config, error) {
	if name == "" {
		return c, nil
	}
	env, ok := c.Environments[name]
	if !ok {
		available := make([]string, 0, len(c.Environments))
		for k := range c.Environments {
			available = append(available, k)
		}
		if len(available) == 0 {
			return nil, fmt.Errorf("environment %q not defined (no environments: block configured)", name)
		}
		return nil, fmt.Errorf("environment %q not defined; available: %s", name, strings.Join(available, ", "))
	}

	out, err := cloneConfig(c)
	if err != nil {
		return nil, fmt.Errorf("resolving environment %q: %w", name, err)
	}

	// Build a set of explicitly disabled source IDs.
	disabledSources := map[string]bool{}
	// Build a map of source overrides for fast lookup.
	overrideByID := make(map[string]EnvironmentSourceOverride, len(env.Sources))
	for _, o := range env.Sources {
		overrideByID[o.ID] = o
		if o.Enabled != nil && !*o.Enabled {
			disabledSources[o.ID] = true
		}
	}

	// Remove disabled sources and apply config overlays.
	filtered := out.Sources[:0]
	for _, src := range out.Sources {
		if disabledSources[src.ID] {
			continue
		}
		if o, ok := overrideByID[src.ID]; ok {
			for k, v := range o.Config {
				if src.Config == nil {
					src.Config = map[string]any{}
				}
				src.Config[k] = v
			}
		}
		filtered = append(filtered, src)
	}
	out.Sources = filtered

	// Apply settings overlay.
	if s := env.Settings; s != nil {
		if s.Concurrency > 0 {
			out.Settings.Concurrency = s.Concurrency
		}
		if s.LogLevel != "" {
			out.Settings.LogLevel = s.LogLevel
		}
		if s.MaxAttempts > 0 {
			out.Settings.MaxAttempts = s.MaxAttempts
		}
	}

	return out, nil
}

// ActiveRollout returns the RolloutConfig for the named environment, or nil
// when no environment is active or the environment has no rollout: block.
func (c *Config) ActiveRollout(envName string) *RolloutConfig {
	if envName == "" || c.Environments == nil {
		return nil
	}
	env, ok := c.Environments[envName]
	if !ok || env.Rollout == nil {
		return nil
	}
	r := *env.Rollout
	return &r
}

// Digest computes a short hex digest of the resolved configuration. The digest
// is stable for identical configs and is recorded on every workflow instance for
// audit purposes. It is not intended for cryptographic use.
func Digest(cfg *Config) (string, error) {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("config digest: %w", err)
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:8]), nil
}

// GitRevision returns the abbreviated Git commit hash at the HEAD of the
// repository containing configPath. Returns an empty string when configPath is
// not inside a Git repository or when git is unavailable — callers must treat
// an empty string as "unknown" and continue normally.
func GitRevision(configPath string) string {
	dir := filepath.Dir(configPath)
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--short=12", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// MarshalYAML returns the YAML encoding of cfg. Used by CLI commands that need
// the serialized form of a resolved environment configuration (e.g. promote,
// rollback).
func MarshalYAML(cfg *Config) ([]byte, error) {
	return yaml.Marshal(cfg)
}

// cloneConfig deep-copies a Config via YAML round-trip so that struct slices
// are not aliased between the original and the copy.
func cloneConfig(c *Config) (*Config, error) {
	data, err := yaml.Marshal(c)
	if err != nil {
		return nil, err
	}
	var out Config
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	// rawContent is used by Save(); propagate it so callers can still persist.
	out.rawContent = c.rawContent
	return &out, nil
}
