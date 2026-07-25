package cli

import (
	"fmt"
	"strings"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/spf13/cobra"
)

func newDiffCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "diff <env1> <env2>",
		Short: "Show a semantic diff between two environment configurations",
		Long: `Compare the resolved configurations of two named environments and display
a human-readable semantic diff grouped by configuration section (sources,
agents, runners, workflows, settings).

Use "base" as either environment name to compare against the base
configuration (no overlays applied).

Examples:
  apiary diff base staging
  apiary diff staging production`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configFile)
			if err != nil {
				return err
			}

			left, err := resolveEnvOrBase(cfg, args[0])
			if err != nil {
				return fmt.Errorf("resolving %q: %w", args[0], err)
			}
			right, err := resolveEnvOrBase(cfg, args[1])
			if err != nil {
				return fmt.Errorf("resolving %q: %w", args[1], err)
			}

			sections := semanticDiff(args[0], args[1], left, right)
			if len(sections) == 0 {
				fmt.Printf("No differences between %q and %q\n", args[0], args[1])
				return nil
			}
			for _, s := range sections {
				fmt.Println(s)
			}
			fmt.Printf("\nDigests: %s=%s  %s=%s\n",
				args[0], left.Digest(),
				args[1], right.Digest(),
			)
			return nil
		},
	}
	return cmd
}

// resolveEnvOrBase returns the named environment's resolved config, or the
// base config unchanged when name is "base".
func resolveEnvOrBase(cfg *config.Config, name string) (*config.Config, error) {
	if strings.ToLower(name) == "base" {
		return cfg, nil
	}
	return cfg.ResolveEnvironment(name)
}

// semanticDiff compares two resolved configs and returns a slice of
// human-readable diff lines grouped by section. Returns nil when identical.
func semanticDiff(leftName, rightName string, left, right *config.Config) []string {
	var lines []string

	// Settings
	if diff := diffSettings(left, right); len(diff) > 0 {
		lines = append(lines, "settings:")
		lines = append(lines, diff...)
	}

	// Sources
	if diff := diffSources(leftName, rightName, left, right); len(diff) > 0 {
		lines = append(lines, "sources:")
		lines = append(lines, diff...)
	}

	// Agents
	if diff := diffAgents(leftName, rightName, left, right); len(diff) > 0 {
		lines = append(lines, "agents:")
		lines = append(lines, diff...)
	}

	// Runners
	if diff := diffRunners(leftName, rightName, left, right); len(diff) > 0 {
		lines = append(lines, "runners:")
		lines = append(lines, diff...)
	}

	// Workflows
	if diff := diffWorkflows(leftName, rightName, left, right); len(diff) > 0 {
		lines = append(lines, "workflows:")
		lines = append(lines, diff...)
	}

	return lines
}

func diffSettings(left, right *config.Config) []string {
	var d []string
	if left.Settings.Concurrency != right.Settings.Concurrency {
		d = append(d, fmt.Sprintf("  concurrency: %d → %d", left.Settings.Concurrency, right.Settings.Concurrency))
	}
	if left.Settings.LogLevel != right.Settings.LogLevel {
		d = append(d, fmt.Sprintf("  log_level: %q → %q", left.Settings.LogLevel, right.Settings.LogLevel))
	}
	if left.Settings.MaxAttempts != right.Settings.MaxAttempts {
		d = append(d, fmt.Sprintf("  max_attempts: %d → %d", left.Settings.MaxAttempts, right.Settings.MaxAttempts))
	}
	return d
}

func diffSources(leftName, rightName string, left, right *config.Config) []string {
	var d []string

	leftIDs := indexByID(sourceIDs(left))
	rightIDs := indexByID(sourceIDs(right))

	for _, id := range sortedKeys(leftIDs) {
		if _, ok := rightIDs[id]; !ok {
			d = append(d, fmt.Sprintf("  - %s (removed in %s)", id, rightName))
		}
	}
	for _, id := range sortedKeys(rightIDs) {
		if _, ok := leftIDs[id]; !ok {
			d = append(d, fmt.Sprintf("  + %s (added in %s)", id, rightName))
		}
	}

	// Config-level differences for sources present in both.
	for _, ls := range left.Sources {
		for _, rs := range right.Sources {
			if ls.ID != rs.ID {
				continue
			}
			for k, lv := range ls.Config {
				rv, ok := rs.Config[k]
				if !ok {
					d = append(d, fmt.Sprintf("  ~ %s.config.%s: %v → (removed)", ls.ID, k, lv))
				} else if fmt.Sprint(lv) != fmt.Sprint(rv) {
					d = append(d, fmt.Sprintf("  ~ %s.config.%s: %v → %v", ls.ID, k, lv, rv))
				}
			}
			for k, rv := range rs.Config {
				if _, ok := ls.Config[k]; !ok {
					d = append(d, fmt.Sprintf("  ~ %s.config.%s: (added) → %v", ls.ID, k, rv))
				}
			}
		}
	}
	return d
}

func diffAgents(leftName, rightName string, left, right *config.Config) []string {
	var d []string

	leftMap := map[string]config.AgentConfig{}
	for _, a := range left.Agents {
		leftMap[a.ID] = a
	}
	rightMap := map[string]config.AgentConfig{}
	for _, a := range right.Agents {
		rightMap[a.ID] = a
	}

	for id, la := range leftMap {
		ra, ok := rightMap[id]
		if !ok {
			d = append(d, fmt.Sprintf("  - %s (removed in %s)", id, rightName))
			continue
		}
		if la.Model != ra.Model {
			d = append(d, fmt.Sprintf("  ~ %s.model: %q → %q", id, la.Model, ra.Model))
		}
		if la.Runner != ra.Runner {
			d = append(d, fmt.Sprintf("  ~ %s.runner: %q → %q", id, la.Runner, ra.Runner))
		}
	}
	for id := range rightMap {
		if _, ok := leftMap[id]; !ok {
			d = append(d, fmt.Sprintf("  + %s (added in %s)", id, rightName))
		}
	}
	return d
}

func diffRunners(leftName, rightName string, left, right *config.Config) []string {
	var d []string

	leftMap := map[string]bool{}
	for _, r := range left.Runners {
		leftMap[r.ID] = true
	}
	rightMap := map[string]bool{}
	for _, r := range right.Runners {
		rightMap[r.ID] = true
	}

	for id := range leftMap {
		if !rightMap[id] {
			d = append(d, fmt.Sprintf("  - %s (removed in %s)", id, rightName))
		}
	}
	for id := range rightMap {
		if !leftMap[id] {
			d = append(d, fmt.Sprintf("  + %s (added in %s)", id, rightName))
		}
	}
	return d
}

func diffWorkflows(leftName, rightName string, left, right *config.Config) []string {
	var d []string

	leftMap := map[string]bool{}
	for _, w := range left.Workflows {
		leftMap[w.ID] = true
	}
	rightMap := map[string]bool{}
	for _, w := range right.Workflows {
		rightMap[w.ID] = true
	}

	for id := range leftMap {
		if !rightMap[id] {
			d = append(d, fmt.Sprintf("  - %s (removed in %s)", id, rightName))
		}
	}
	for id := range rightMap {
		if !leftMap[id] {
			d = append(d, fmt.Sprintf("  + %s (added in %s)", id, rightName))
		}
	}
	return d
}

// sourceIDs returns the IDs of all sources in a config.
func sourceIDs(cfg *config.Config) []string {
	ids := make([]string, 0, len(cfg.Sources))
	for _, s := range cfg.Sources {
		ids = append(ids, s.ID)
	}
	return ids
}

// indexByID turns a slice of IDs into a presence map.
func indexByID(ids []string) map[string]struct{} {
	m := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		m[id] = struct{}{}
	}
	return m
}

// sortedKeys returns the keys of a map in sorted order.
func sortedKeys(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Simple insertion sort (maps are small).
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
