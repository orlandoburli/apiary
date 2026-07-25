package config

import (
	"fmt"
	"strings"
)

// DiffEntry describes one semantic difference between two resolved configs.
type DiffEntry struct {
	// Kind is the entity type: "workflow", "source", "agent", "runner", "setting".
	Kind string
	// Name is the entity ID or setting key.
	Name string
	// Op is the change operation: "added", "removed", or "changed".
	Op string
	// Detail is a human-readable description of what changed (only for "changed").
	Detail string
}

func (d DiffEntry) String() string {
	switch d.Op {
	case "changed":
		return fmt.Sprintf("%s %s: %s (%s)", d.Kind, d.Name, d.Op, d.Detail)
	default:
		return fmt.Sprintf("%s %s: %s", d.Kind, d.Name, d.Op)
	}
}

// SemanticDiff computes the differences between two resolved configurations.
// The diff is entity-aware: it understands workflows, sources, agents, runners,
// and settings rather than treating the config as raw YAML text. This makes it
// meaningful across environments where the same entity may have different
// credentials or concurrency limits.
func SemanticDiff(from, to *Config) []DiffEntry {
	var entries []DiffEntry
	entries = append(entries, diffWorkflows(from, to)...)
	entries = append(entries, diffSources(from, to)...)
	entries = append(entries, diffAgents(from, to)...)
	entries = append(entries, diffRunners(from, to)...)
	entries = append(entries, diffSettings(from, to)...)
	return entries
}

func diffWorkflows(from, to *Config) []DiffEntry {
	fromMap := make(map[string]WorkflowConfig, len(from.Workflows))
	for _, w := range from.Workflows {
		fromMap[w.ID] = w
	}
	toMap := make(map[string]WorkflowConfig, len(to.Workflows))
	for _, w := range to.Workflows {
		toMap[w.ID] = w
	}
	var out []DiffEntry
	for id := range fromMap {
		if _, ok := toMap[id]; !ok {
			out = append(out, DiffEntry{Kind: "workflow", Name: id, Op: "removed"})
		}
	}
	for id, tw := range toMap {
		fw, ok := fromMap[id]
		if !ok {
			out = append(out, DiffEntry{Kind: "workflow", Name: id, Op: "added"})
			continue
		}
		var changes []string
		if len(fw.Steps) != len(tw.Steps) {
			changes = append(changes, fmt.Sprintf("steps: %d → %d", len(fw.Steps), len(tw.Steps)))
		}
		if fw.Trigger.Priority != tw.Trigger.Priority {
			changes = append(changes, fmt.Sprintf("trigger.priority: %d → %d", fw.Trigger.Priority, tw.Trigger.Priority))
		}
		if len(changes) > 0 {
			out = append(out, DiffEntry{Kind: "workflow", Name: id, Op: "changed",
				Detail: strings.Join(changes, "; ")})
		}
	}
	return out
}

func diffSources(from, to *Config) []DiffEntry {
	fromMap := make(map[string]SourceConfig, len(from.Sources))
	for _, s := range from.Sources {
		fromMap[s.ID] = s
	}
	toMap := make(map[string]SourceConfig, len(to.Sources))
	for _, s := range to.Sources {
		toMap[s.ID] = s
	}
	var out []DiffEntry
	for id := range fromMap {
		if _, ok := toMap[id]; !ok {
			out = append(out, DiffEntry{Kind: "source", Name: id, Op: "removed"})
		}
	}
	for id, ts := range toMap {
		fs, ok := fromMap[id]
		if !ok {
			out = append(out, DiffEntry{Kind: "source", Name: id, Op: "added"})
			continue
		}
		var changes []string
		if fs.Type != ts.Type {
			changes = append(changes, fmt.Sprintf("type: %s → %s", fs.Type, ts.Type))
		}
		if fs.PollInterval != ts.PollInterval {
			changes = append(changes, fmt.Sprintf("poll_interval: %s → %s", fs.PollInterval, ts.PollInterval))
		}
		if len(changes) > 0 {
			out = append(out, DiffEntry{Kind: "source", Name: id, Op: "changed",
				Detail: strings.Join(changes, "; ")})
		}
	}
	return out
}

func diffAgents(from, to *Config) []DiffEntry {
	fromMap := make(map[string]AgentConfig, len(from.Agents))
	for _, a := range from.Agents {
		fromMap[a.ID] = a
	}
	toMap := make(map[string]AgentConfig, len(to.Agents))
	for _, a := range to.Agents {
		toMap[a.ID] = a
	}
	var out []DiffEntry
	for id := range fromMap {
		if _, ok := toMap[id]; !ok {
			out = append(out, DiffEntry{Kind: "agent", Name: id, Op: "removed"})
		}
	}
	for id, ta := range toMap {
		fa, ok := fromMap[id]
		if !ok {
			out = append(out, DiffEntry{Kind: "agent", Name: id, Op: "added"})
			continue
		}
		var changes []string
		if fa.Model != ta.Model {
			changes = append(changes, fmt.Sprintf("model: %s → %s", fa.Model, ta.Model))
		}
		if fa.Runner != ta.Runner {
			changes = append(changes, fmt.Sprintf("runner: %s → %s", fa.Runner, ta.Runner))
		}
		if fa.MaxWorkers != ta.MaxWorkers {
			changes = append(changes, fmt.Sprintf("max_workers: %d → %d", fa.MaxWorkers, ta.MaxWorkers))
		}
		if len(changes) > 0 {
			out = append(out, DiffEntry{Kind: "agent", Name: id, Op: "changed",
				Detail: strings.Join(changes, "; ")})
		}
	}
	return out
}

func diffRunners(from, to *Config) []DiffEntry {
	fromMap := make(map[string]RunnerConfig, len(from.Runners))
	for _, r := range from.Runners {
		fromMap[r.ID] = r
	}
	toMap := make(map[string]RunnerConfig, len(to.Runners))
	for _, r := range to.Runners {
		toMap[r.ID] = r
	}
	var out []DiffEntry
	for id := range fromMap {
		if _, ok := toMap[id]; !ok {
			out = append(out, DiffEntry{Kind: "runner", Name: id, Op: "removed"})
		}
	}
	for id, tr := range toMap {
		fr, ok := fromMap[id]
		if !ok {
			out = append(out, DiffEntry{Kind: "runner", Name: id, Op: "added"})
			continue
		}
		if fr.Type != tr.Type || fr.Provider != tr.Provider {
			out = append(out, DiffEntry{Kind: "runner", Name: id, Op: "changed",
				Detail: fmt.Sprintf("type/provider: %s/%s → %s/%s", fr.Type, fr.Provider, tr.Type, tr.Provider)})
		}
	}
	return out
}

func diffSettings(from, to *Config) []DiffEntry {
	var out []DiffEntry
	if from.Settings.Concurrency != to.Settings.Concurrency {
		out = append(out, DiffEntry{Kind: "setting", Name: "concurrency", Op: "changed",
			Detail: fmt.Sprintf("%d → %d", from.Settings.Concurrency, to.Settings.Concurrency)})
	}
	if from.Settings.LogLevel != to.Settings.LogLevel {
		out = append(out, DiffEntry{Kind: "setting", Name: "log_level", Op: "changed",
			Detail: fmt.Sprintf("%q → %q", from.Settings.LogLevel, to.Settings.LogLevel)})
	}
	if from.Settings.MaxAttempts != to.Settings.MaxAttempts {
		out = append(out, DiffEntry{Kind: "setting", Name: "max_attempts", Op: "changed",
			Detail: fmt.Sprintf("%d → %d", from.Settings.MaxAttempts, to.Settings.MaxAttempts)})
	}
	if from.Settings.StateLock != to.Settings.StateLock {
		out = append(out, DiffEntry{Kind: "setting", Name: "state_lock", Op: "changed",
			Detail: fmt.Sprintf("%v → %v", from.Settings.StateLock, to.Settings.StateLock)})
	}
	return out
}
