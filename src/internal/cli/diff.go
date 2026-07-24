package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/spf13/cobra"
)

func newDiffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diff <env1> <env2>",
		Short: "Show semantic diff between two environment configurations",
		Long: `diff compares two resolved configurations and prints a human-readable
summary of the differences. Use "base" to refer to the unmodified config.

Examples:
  apiary diff base staging
  apiary diff staging production`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configFile)
			if err != nil {
				return err
			}

			left, err := resolveEnvConfig(cfg, args[0])
			if err != nil {
				return fmt.Errorf("resolving %q: %w", args[0], err)
			}
			right, err := resolveEnvConfig(cfg, args[1])
			if err != nil {
				return fmt.Errorf("resolving %q: %w", args[1], err)
			}

			lines := semanticDiff(args[0], args[1], left, right)
			if len(lines) == 0 {
				fmt.Printf("No differences between %q and %q\n", args[0], args[1])
				return nil
			}
			fmt.Printf("Diff: %s → %s\n\n", args[0], args[1])
			for _, l := range lines {
				fmt.Println(l)
			}
			return nil
		},
	}
}

// resolveEnvConfig returns the resolved config for an environment name.
// "base" returns the base config unchanged.
func resolveEnvConfig(cfg *config.Config, name string) (*config.Config, error) {
	if name == "base" {
		return cfg, nil
	}
	return cfg.ForEnvironment(name)
}

// semanticDiff returns human-readable diff lines comparing two configs.
func semanticDiff(leftName, rightName string, left, right *config.Config) []string {
	var out []string

	// Settings.
	if left.Settings.Concurrency != right.Settings.Concurrency {
		out = append(out, fmt.Sprintf("  settings.concurrency: %d → %d", left.Settings.Concurrency, right.Settings.Concurrency))
	}
	if left.Settings.LogLevel != right.Settings.LogLevel {
		out = append(out, fmt.Sprintf("  settings.log_level: %q → %q", left.Settings.LogLevel, right.Settings.LogLevel))
	}
	if left.Settings.MaxAttempts != right.Settings.MaxAttempts {
		out = append(out, fmt.Sprintf("  settings.max_attempts: %d → %d", left.Settings.MaxAttempts, right.Settings.MaxAttempts))
	}

	// Sources: added, removed, changed.
	leftSrc := sourcesMap(left)
	rightSrc := sourcesMap(right)

	for id := range rightSrc {
		if _, ok := leftSrc[id]; !ok {
			out = append(out, fmt.Sprintf("  + source %q (added)", id))
		}
	}
	for id := range leftSrc {
		if _, ok := rightSrc[id]; !ok {
			out = append(out, fmt.Sprintf("  - source %q (removed)", id))
		}
	}
	for id, ls := range leftSrc {
		rs, ok := rightSrc[id]
		if !ok {
			continue
		}
		if ls.PollInterval != rs.PollInterval {
			out = append(out, fmt.Sprintf("  source %q poll_interval: %q → %q", id, ls.PollInterval, rs.PollInterval))
		}
		for k, lv := range ls.Config {
			rv, ok := rs.Config[k]
			if !ok {
				out = append(out, fmt.Sprintf("  source %q config.%s: %v → (removed)", id, k, lv))
			} else if fmt.Sprintf("%v", lv) != fmt.Sprintf("%v", rv) {
				out = append(out, fmt.Sprintf("  source %q config.%s: %v → %v", id, k, lv, rv))
			}
		}
		for k, rv := range rs.Config {
			if _, ok := ls.Config[k]; !ok {
				out = append(out, fmt.Sprintf("  source %q config.%s: (added) %v", id, k, rv))
			}
		}
	}

	// Agents: model, runner changes.
	leftAgents := agentsMap(left)
	rightAgents := agentsMap(right)

	for id := range rightAgents {
		if _, ok := leftAgents[id]; !ok {
			out = append(out, fmt.Sprintf("  + agent %q (added)", id))
		}
	}
	for id := range leftAgents {
		if _, ok := rightAgents[id]; !ok {
			out = append(out, fmt.Sprintf("  - agent %q (removed)", id))
		}
	}
	for id, la := range leftAgents {
		ra, ok := rightAgents[id]
		if !ok {
			continue
		}
		if la.Model != ra.Model {
			out = append(out, fmt.Sprintf("  agent %q model: %q → %q", id, la.Model, ra.Model))
		}
		if la.Runner != ra.Runner {
			out = append(out, fmt.Sprintf("  agent %q runner: %q → %q", id, la.Runner, ra.Runner))
		}
		if la.MaxWorkers != ra.MaxWorkers {
			out = append(out, fmt.Sprintf("  agent %q max_workers: %d → %d", id, la.MaxWorkers, ra.MaxWorkers))
		}
		envDiff := diffStringMap("agent "+id+" env", la.Env, ra.Env)
		out = append(out, envDiff...)
	}

	// Runners: config changes.
	leftRunners := runnersMap(left)
	rightRunners := runnersMap(right)

	for id, lr := range leftRunners {
		rr, ok := rightRunners[id]
		if !ok {
			out = append(out, fmt.Sprintf("  - runner %q (removed)", id))
			continue
		}
		for k, lv := range lr.Config {
			rv, ok := rr.Config[k]
			if !ok {
				out = append(out, fmt.Sprintf("  runner %q config.%s: %v → (removed)", id, k, lv))
			} else if fmt.Sprintf("%v", lv) != fmt.Sprintf("%v", rv) {
				out = append(out, fmt.Sprintf("  runner %q config.%s: %v → %v", id, k, lv, rv))
			}
		}
		for k, rv := range rr.Config {
			if _, ok := lr.Config[k]; !ok {
				out = append(out, fmt.Sprintf("  runner %q config.%s: (added) %v", id, k, rv))
			}
		}
	}
	for id := range rightRunners {
		if _, ok := leftRunners[id]; !ok {
			out = append(out, fmt.Sprintf("  + runner %q (added)", id))
		}
	}

	// Workflows: added / removed.
	leftWFs := workflowSet(left)
	rightWFs := workflowSet(right)
	for id := range rightWFs {
		if !leftWFs[id] {
			out = append(out, fmt.Sprintf("  + workflow %q (added)", id))
		}
	}
	for id := range leftWFs {
		if !rightWFs[id] {
			out = append(out, fmt.Sprintf("  - workflow %q (removed)", id))
		}
	}

	// Config digest.
	ld := config.Digest(left)
	rd := config.Digest(right)
	if ld == rd {
		out = append(out, fmt.Sprintf("\n  digest: %s (identical)", ld[:16]))
	} else {
		out = append(out, fmt.Sprintf("\n  digest: %s → %s", ld[:16], rd[:16]))
	}

	sort.Strings(out[:len(out)-1]) // sort all but the digest line
	return out
}

func sourcesMap(cfg *config.Config) map[string]config.SourceConfig {
	m := make(map[string]config.SourceConfig, len(cfg.Sources))
	for _, s := range cfg.Sources {
		m[s.ID] = s
	}
	return m
}

func agentsMap(cfg *config.Config) map[string]config.AgentConfig {
	m := make(map[string]config.AgentConfig, len(cfg.Agents))
	for _, a := range cfg.Agents {
		m[a.ID] = a
	}
	return m
}

func runnersMap(cfg *config.Config) map[string]config.RunnerConfig {
	m := make(map[string]config.RunnerConfig, len(cfg.Runners))
	for _, r := range cfg.Runners {
		m[r.ID] = r
	}
	return m
}

func workflowSet(cfg *config.Config) map[string]bool {
	m := make(map[string]bool, len(cfg.Workflows))
	for _, w := range cfg.Workflows {
		m[w.ID] = true
	}
	return m
}

func diffStringMap(label string, left, right map[string]string) []string {
	var out []string
	allKeys := make(map[string]bool)
	for k := range left {
		allKeys[k] = true
	}
	for k := range right {
		allKeys[k] = true
	}
	keys := make([]string, 0, len(allKeys))
	for k := range allKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		lv := left[k]
		rv := right[k]
		if lv == rv {
			continue
		}
		lDisplay := maskSecret(k, lv)
		rDisplay := maskSecret(k, rv)
		if lv == "" {
			out = append(out, fmt.Sprintf("  %s.%s: (added) %s", label, k, rDisplay))
		} else if rv == "" {
			out = append(out, fmt.Sprintf("  %s.%s: %s → (removed)", label, k, lDisplay))
		} else {
			out = append(out, fmt.Sprintf("  %s.%s: %s → %s", label, k, lDisplay, rDisplay))
		}
	}
	return out
}

// maskSecret redacts values for keys that look like credentials or tokens.
func maskSecret(key, value string) string {
	lower := strings.ToLower(key)
	for _, word := range []string{"token", "secret", "password", "key", "credential", "auth"} {
		if strings.Contains(lower, word) {
			return "***"
		}
	}
	// Mask $VAR references so we don't print unexpanded variables.
	if strings.HasPrefix(value, "${") {
		return value // show the reference name, not the resolved value
	}
	return value
}
