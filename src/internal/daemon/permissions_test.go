package daemon

import (
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
)

// Default must stay permissive: these files are rewritten on every dispatch, so
// flipping the default would silently strip edit/bash from every existing agent,
// which fails by producing no diff rather than by erroring.
func TestAgentPermissions_DefaultIsPermissive(t *testing.T) {
	p := agentPermissions(config.AgentConfig{ID: "eng"}, false)
	for _, tool := range agentTools {
		if p[tool] != "allow" {
			t.Errorf("default should allow %q, got %q", tool, p[tool])
		}
	}
}

func TestAgentPermissions_LeastPrivilegeOptIn(t *testing.T) {
	p := agentPermissions(config.AgentConfig{ID: "eng"}, true)
	for _, tool := range []string{"read", "glob", "grep", "task"} {
		if p[tool] != "allow" {
			t.Errorf("read-only tool %q should stay allow, got %q", tool, p[tool])
		}
	}
	for _, tool := range []string{"edit", "bash", "webfetch"} {
		if p[tool] != "deny" {
			t.Errorf("least-privilege should deny %q, got %q", tool, p[tool])
		}
	}
}

// An explicit per-agent entry wins in both directions, so one agent can be
// locked down without changing the global default, and one agent can keep shell
// access when the global default is least-privilege.
func TestAgentPermissions_ExplicitOverridesBaseline(t *testing.T) {
	restricted := agentPermissions(config.AgentConfig{
		ID: "reviewer", Permissions: map[string]string{"bash": "deny", "edit": "deny"},
	}, false)
	if restricted["bash"] != "deny" || restricted["edit"] != "deny" {
		t.Errorf("explicit deny ignored under permissive default: %v", restricted)
	}

	granted := agentPermissions(config.AgentConfig{
		ID: "eng", Permissions: map[string]string{"bash": "allow", "edit": "allow"},
	}, true)
	if granted["bash"] != "allow" || granted["edit"] != "allow" {
		t.Errorf("explicit allow ignored under least-privilege: %v", granted)
	}
	if granted["webfetch"] != "deny" {
		t.Errorf("unspecified tool should keep the least-privilege baseline, got %q", granted["webfetch"])
	}
}

// The JSON opencode.json writer historically omitted webfetch. Emitting it on
// the permissive default path would be a net privilege INCREASE, so the
// historical key set is preserved unless least-privilege is on or the agent
// sets it explicitly.
func TestPermissionMap_NoPrivilegeIncreaseOnDefaultPath(t *testing.T) {
	def := permissionMap(config.AgentConfig{ID: "eng"}, false)
	if _, present := def["webfetch"]; present {
		t.Errorf("permissive default must not newly grant webfetch in opencode.json: %v", def)
	}
	for _, tool := range []string{"read", "glob", "grep", "task", "edit", "bash"} {
		if def[tool] != "allow" {
			t.Errorf("expected %q allow on default path, got %v", tool, def[tool])
		}
	}

	// Least-privilege writes the full set (webfetch explicitly denied).
	lp := permissionMap(config.AgentConfig{ID: "eng"}, true)
	if lp["webfetch"] != "deny" {
		t.Errorf("least-privilege should write webfetch: deny, got %v", lp["webfetch"])
	}

	// An explicit entry is always honoured.
	got := permissionMap(config.AgentConfig{ID: "eng", Permissions: map[string]string{"webfetch": "allow"}}, false)
	if got["webfetch"] != "allow" {
		t.Errorf("explicit webfetch entry must be written, got %v", got["webfetch"])
	}
}
