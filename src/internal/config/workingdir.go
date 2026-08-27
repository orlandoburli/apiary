package config

import (
	"os"
	"path/filepath"
	"strings"
)

// ResolveWorkingDir picks the process working directory for one agent step.
//
// Precedence, first non-empty wins:
//
//	step.working_dir
//	workflow.working_dir
//	agents[].working_dir
//	runners[].config.working_dir   (the agent's runner, or default_runner)
//	the directory holding apiary.yaml
//
// The chain never yields "/" by default: an agent with no cwd of its own has to
// go looking for the repository, which wastes turns, wanders into the operator's
// home directory, and can settle on the wrong checkout (#436). The config's own
// directory is a bounded, operator-chosen starting point.
//
// Relative values are resolved against the config directory, and a leading "~"
// is expanded to the user's home. When the Config carries no directory of its
// own (built in memory rather than loaded from disk), the process working
// directory is used as the base and as the final fallback.
func (c *Config) ResolveWorkingDir(agent AgentConfig, workflowDir, stepDir string) string {
	base := c.Dir()
	if base == "" {
		if wd, err := os.Getwd(); err == nil {
			base = wd
		}
	}

	for _, candidate := range []string{stepDir, workflowDir, agent.WorkingDir, c.runnerWorkingDir(agent)} {
		if dir := expandDir(candidate, base); dir != "" {
			return dir
		}
	}
	return base
}

// runnerWorkingDir returns the working_dir declared in the config block of the
// runner this agent uses (its own, or the global default_runner).
func (c *Config) runnerWorkingDir(agent AgentConfig) string {
	if c == nil {
		return ""
	}
	id := agent.Runner
	if id == "" {
		id = c.DefaultRunner
	}
	if id == "" {
		return ""
	}
	for i := range c.Runners {
		if c.Runners[i].ID != id {
			continue
		}
		dir, _ := c.Runners[i].Config["working_dir"].(string)
		return dir
	}
	return ""
}

// expandDir expands "~" and makes a relative path absolute against base.
// It returns "" for an empty input so callers can walk a precedence chain.
func expandDir(dir, base string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(dir, "~"), "/"))
		}
	}
	if !filepath.IsAbs(dir) && base != "" {
		dir = filepath.Join(base, dir)
	}
	return filepath.Clean(dir)
}
