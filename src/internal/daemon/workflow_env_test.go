package daemon

import (
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
)

// TestStepEnv_Precedence exercises the STEP > WORKFLOW > AGENT precedence of the
// per-scope env merge, layered on top of the identity base (git + source_token).
func TestStepEnv_Precedence(t *testing.T) {
	agent := config.AgentConfig{
		SourceName:  "reviewer-bot",
		SourceEmail: "reviewer@example.com",
		SourceToken: "ghp_agenttoken",
		Env: map[string]string{
			"SCOPE":      "agent",
			"AGENT_ONLY": "a",
		},
	}
	wfEnv := map[string]string{
		"SCOPE":   "workflow",
		"WF_ONLY": "w",
	}
	stEnv := map[string]string{
		"SCOPE":     "step",
		"STEP_ONLY": "s",
	}

	env := stepEnv(agent, wfEnv, stEnv)

	cases := map[string]string{
		// Identity base survives when no explicit scope overrides it.
		"GIT_AUTHOR_NAME":     "reviewer-bot",
		"GIT_COMMITTER_EMAIL": "reviewer@example.com",
		"GITHUB_TOKEN":        "ghp_agenttoken",
		"GH_TOKEN":            "ghp_agenttoken",
		// Each scope contributes its unique keys.
		"AGENT_ONLY": "a",
		"WF_ONLY":    "w",
		"STEP_ONLY":  "s",
		// Step wins the shared key over workflow and agent.
		"SCOPE": "step",
	}
	for k, want := range cases {
		if env[k] != want {
			t.Errorf("env[%q] = %q, want %q", k, env[k], want)
		}
	}
}

// TestStepEnv_WorkflowOverridesAgent verifies the middle layer: with no step
// env, a workflow value overrides the same agent key while agent-only keys
// survive.
func TestStepEnv_WorkflowOverridesAgent(t *testing.T) {
	agent := config.AgentConfig{
		Env: map[string]string{"SCOPE": "agent", "AGENT_ONLY": "a"},
	}
	env := stepEnv(agent, map[string]string{"SCOPE": "workflow"}, nil)

	if env["SCOPE"] != "workflow" {
		t.Errorf("SCOPE = %q, want workflow", env["SCOPE"])
	}
	if env["AGENT_ONLY"] != "a" {
		t.Errorf("AGENT_ONLY = %q, want a (agent-only key should survive)", env["AGENT_ONLY"])
	}
}

// TestStepEnv_ExplicitTokenOverridesIdentity verifies the deliberate escape
// hatch: an explicit step-scope GITHUB_TOKEN overrides the source_token-derived
// identity overlay.
func TestStepEnv_ExplicitTokenOverridesIdentity(t *testing.T) {
	agent := config.AgentConfig{SourceToken: "ghp_agenttoken"}
	env := stepEnv(agent, nil, map[string]string{"GITHUB_TOKEN": "ghp_override"})

	if env["GITHUB_TOKEN"] != "ghp_override" {
		t.Errorf("GITHUB_TOKEN = %q, want ghp_override (step env should override identity)", env["GITHUB_TOKEN"])
	}
	// GH_TOKEN was not overridden, so the identity value remains.
	if env["GH_TOKEN"] != "ghp_agenttoken" {
		t.Errorf("GH_TOKEN = %q, want ghp_agenttoken (identity layer untouched)", env["GH_TOKEN"])
	}
}

// TestStepEnv_NoExplicitEnv verifies that with no env at any scope, the result is
// exactly the identity overlay — a regression guard that the merge is a no-op by
// default.
func TestStepEnv_NoExplicitEnv(t *testing.T) {
	agent := config.AgentConfig{
		SourceName:  "reviewer-bot",
		SourceEmail: "reviewer@example.com",
		SourceToken: "ghp_agenttoken",
	}
	got := stepEnv(agent, nil, nil)
	want := agentIdentityEnv(agent)

	if len(got) != len(want) {
		t.Fatalf("len(stepEnv) = %d, want %d (identity overlay only): %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("env[%q] = %q, want %q", k, got[k], v)
		}
	}
}
