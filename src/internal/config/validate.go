package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
)

// KnownAdapters reports the registered runner adapter names. The cli package
// injects it (config cannot import the runner package without inverting the
// dependency direction). When nil — configs built in code, isolated tests —
// the adapter check is skipped.
var KnownAdapters func() []string

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

	// settings.memory value checks (the block itself is optional).
	if m := c.Settings.Memory; m.TaskRetention != "" {
		if _, err := time.ParseDuration(m.TaskRetention); err != nil {
			errs = append(errs, fmt.Errorf("settings.memory.task_retention %q: invalid duration: %w", m.TaskRetention, err))
		}
	}
	if c.Settings.Memory.MaxInjectChars < 0 {
		errs = append(errs, fmt.Errorf("settings.memory.max_inject_chars must be >= 0"))
	}
	if c.Settings.Memory.MaxEntryBytes < 0 {
		errs = append(errs, fmt.Errorf("settings.memory.max_entry_bytes must be >= 0"))
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

	return errs
}
