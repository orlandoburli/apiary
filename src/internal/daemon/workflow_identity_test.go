package daemon

import (
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
)

// TestAgentIdentityEnv_WithSourceToken verifies that an agent declaring a
// source_token gets it exported to the subprocess as both GITHUB_TOKEN and
// GH_TOKEN (so `gh` commands the agent runs authenticate as the agent's own
// account), alongside the git author/committer identity. Regression for
// orlandoburli-enterprise/project-erp#1948.
func TestAgentIdentityEnv_WithSourceToken(t *testing.T) {
	env := agentIdentityEnv(config.AgentConfig{
		SourceName:  "orlandodeveloper01",
		SourceEmail: "orlando.developer01@gmail.com",
		SourceToken: "ghp_reviewertoken",
	})

	want := map[string]string{
		"GIT_AUTHOR_NAME":     "orlandodeveloper01",
		"GIT_COMMITTER_NAME":  "orlandodeveloper01",
		"GIT_AUTHOR_EMAIL":    "orlando.developer01@gmail.com",
		"GIT_COMMITTER_EMAIL": "orlando.developer01@gmail.com",
		"GITHUB_TOKEN":        "ghp_reviewertoken",
		"GH_TOKEN":            "ghp_reviewertoken",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("env[%q] = %q, want %q", k, env[k], v)
		}
	}
}

// TestAgentIdentityEnv_NoSourceToken verifies that without a source_token, no
// GitHub token env vars are emitted, so the agent falls back to the daemon's
// inherited credentials. The git identity is still set from name/email.
func TestAgentIdentityEnv_NoSourceToken(t *testing.T) {
	env := agentIdentityEnv(config.AgentConfig{
		SourceName:  "orlandodeveloper01",
		SourceEmail: "orlando.developer01@gmail.com",
	})

	if _, ok := env["GITHUB_TOKEN"]; ok {
		t.Errorf("GITHUB_TOKEN should be absent without a source_token, got %q", env["GITHUB_TOKEN"])
	}
	if _, ok := env["GH_TOKEN"]; ok {
		t.Errorf("GH_TOKEN should be absent without a source_token, got %q", env["GH_TOKEN"])
	}
	if env["GIT_AUTHOR_NAME"] != "orlandodeveloper01" {
		t.Errorf("GIT_AUTHOR_NAME = %q, want orlandodeveloper01", env["GIT_AUTHOR_NAME"])
	}
	if env["GIT_COMMITTER_EMAIL"] != "orlando.developer01@gmail.com" {
		t.Errorf("GIT_COMMITTER_EMAIL = %q, want orlando.developer01@gmail.com", env["GIT_COMMITTER_EMAIL"])
	}
}
