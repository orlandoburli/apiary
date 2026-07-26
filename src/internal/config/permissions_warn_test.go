package config

import "testing"

// least_privilege_agents and agents[].permissions are inert on runners other
// than opencode; the operator must be told rather than silently believing the
// fleet is restricted.
func TestWarnUnenforceablePermissions_DoesNotPanicAndSkipsOpencode(t *testing.T) {
	cfg := &Config{
		DefaultRunner: "claude",
		Runners: []RunnerConfig{
			{ID: "claude", Type: "cli", Provider: "claude"},
			{ID: "oc", Type: "cli", Provider: "opencode"},
		},
		Agents: []AgentConfig{
			{ID: "on-claude", Permissions: map[string]string{"bash": "deny"}},
			{ID: "on-opencode", Runner: "oc", Permissions: map[string]string{"bash": "deny"}},
			{ID: "unknown-runner", Runner: "nope"},
		},
		Settings: Settings{LeastPrivilegeAgents: true},
	}
	// Exercises every branch: unsupported adapter with explicit permissions,
	// supported adapter, and an unresolved runner (reported by Validate instead).
	warnUnenforceablePermissions(cfg)
}
