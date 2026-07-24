package config_test

import (
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
)

func TestValidate_Valid(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Sources: []config.SourceConfig{
			{ID: "src-1", Type: "plane"},
		},
		Agents: []config.AgentConfig{
			{ID: "a-1", Model: "claude-sonnet-4-6"},
		},
		Workflows: []config.WorkflowConfig{{
			ID:      "wf-1",
			Trigger: &config.TriggerConfig{Priority: 1, Match: config.RouteMatch{Source: "src-1"}},
			Steps:   []config.StepConfig{{ID: "run", Agent: "a-1"}},
		}},
	}
	if errs := cfg.Validate(); len(errs) != 0 {
		t.Errorf("expected no errors, got: %v", errs)
	}
}

func TestValidate_MissingVersion(t *testing.T) {
	cfg := &config.Config{}
	assertError(t, cfg, "version is required")
}

func TestValidate_MCPMissingCommand(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Runners: []config.RunnerConfig{
			{ID: "claude", Type: "cli", MCPs: []model.MCPServer{{Name: "gitnexus"}}},
		},
	}
	assertError(t, cfg, "command is required")
}

func TestValidate_MCPMissingName(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Agents: []config.AgentConfig{
			{ID: "a-1", Model: "m", MCPs: []model.MCPServer{{Command: "npx"}}},
		},
	}
	assertError(t, cfg, "name is required")
}

func TestValidate_MCPDuplicateName(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Runners: []config.RunnerConfig{
			{ID: "claude", Type: "cli", MCPs: []model.MCPServer{
				{Name: "gitnexus", Command: "npx"},
				{Name: "gitnexus", Command: "npx"},
			}},
		},
	}
	assertError(t, cfg, "duplicate name")
}

func TestValidate_MCPCommandNotInAllowList(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Runners: []config.RunnerConfig{
			{ID: "claude", Type: "cli", MCPs: []model.MCPServer{
				{Name: "evil", Command: "/bin/bash"},
			}},
		},
	}
	assertError(t, cfg, "not in the allow-list")
}

func TestValidate_MCPCommandAllowedOnRunner(t *testing.T) {
	for _, cmd := range []string{"npx", "node", "python", "python3", "uvx", "docker", "bunx", "deno"} {
		cfg := &config.Config{
			Version: "1",
			Runners: []config.RunnerConfig{
				{ID: "r", Type: "cli", MCPs: []model.MCPServer{{Name: "srv", Command: cmd}}},
			},
		}
		errs := cfg.Validate()
		for _, e := range errs {
			if strings.Contains(e.Error(), "allow-list") {
				t.Errorf("command %q rejected unexpectedly: %v", cmd, e)
			}
		}
	}
}

func TestValidate_MCPCommandNotInAllowListOnAgent(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Agents: []config.AgentConfig{
			{ID: "a-1", Model: "m", MCPs: []model.MCPServer{{Name: "srv", Command: "curl"}}},
		},
	}
	assertError(t, cfg, "not in the allow-list")
}

func TestValidate_MissingSourceID(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Sources: []config.SourceConfig{{Type: "plane"}},
	}
	assertError(t, cfg, "id is required")
}

func TestValidate_MissingSourceType(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Sources: []config.SourceConfig{{ID: "src-1"}},
	}
	assertError(t, cfg, "type is required")
}

func TestValidate_DuplicateSourceID(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Sources: []config.SourceConfig{
			{ID: "dup", Type: "plane"},
			{ID: "dup", Type: "jira"},
		},
	}
	assertError(t, cfg, "duplicate id")
}

func TestValidate_MissingWorkerRunner(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Workers: []config.WorkerConfig{{ID: "w-1", Model: "x"}},
	}
	assertError(t, cfg, "runner is required")
}

func TestValidate_MissingWorkerModel(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Workers: []config.WorkerConfig{{ID: "w-1", Runner: "cli"}},
	}
	assertError(t, cfg, "model is required")
}

func TestValidate_DuplicateWorkerID(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Workers: []config.WorkerConfig{
			{ID: "dup", Runner: "cli", Model: "x"},
			{ID: "dup", Runner: "cli", Model: "y"},
		},
	}
	assertError(t, cfg, "duplicate id")
}

func TestValidate_WorkflowReferencesUnknownAgent(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Sources: []config.SourceConfig{{ID: "src-1", Type: "plane"}},
		Agents:  []config.AgentConfig{{ID: "a-1", Model: "claude-sonnet-4-6"}},
		Workflows: []config.WorkflowConfig{{
			ID:      "wf-1",
			Trigger: &config.TriggerConfig{Match: config.RouteMatch{Source: "src-1"}},
			Steps:   []config.StepConfig{{ID: "run", Agent: "nonexistent"}},
		}},
	}
	assertError(t, cfg, "not defined")
}

func TestValidate_WorkflowReferencesUnknownSource(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Sources: []config.SourceConfig{{ID: "src-1", Type: "plane"}},
		Agents:  []config.AgentConfig{{ID: "a-1", Model: "claude-sonnet-4-6"}},
		Workflows: []config.WorkflowConfig{{
			ID:      "wf-1",
			Trigger: &config.TriggerConfig{Match: config.RouteMatch{Source: "nonexistent"}},
			Steps:   []config.StepConfig{{ID: "run", Agent: "a-1"}},
		}},
	}
	assertError(t, cfg, "not defined")
}

func TestValidate_MultipleErrors(t *testing.T) {
	cfg := &config.Config{} // missing version + no sources/workers is still valid structurally
	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected at least one error")
	}
}

// assertError checks that at least one validation error contains the given substring.
func assertError(t *testing.T, cfg *config.Config, contains string) {
	t.Helper()
	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatalf("expected validation error containing %q, got none", contains)
	}
	for _, e := range errs {
		if containsStr(e.Error(), contains) {
			return
		}
	}
	t.Errorf("no error contained %q — errors were: %v", contains, errs)
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}

func TestValidate_GitHooksPairing(t *testing.T) {
	base := func() *config.Config {
		return &config.Config{
			Version: "1",
			Sources: []config.SourceConfig{{ID: "src-1", Type: "plane"}},
			Agents:  []config.AgentConfig{{ID: "a-1", Model: "claude-sonnet-4-6"}},
			Workflows: []config.WorkflowConfig{{
				ID:      "wf-1",
				Trigger: &config.TriggerConfig{Priority: 1, Match: config.RouteMatch{Source: "src-1"}},
				Steps:   []config.StepConfig{{ID: "run", Agent: "a-1"}},
			}},
		}
	}

	cfg := base()
	cfg.Settings.GitHooks = config.GitHooksSettings{Dir: ".apiary/hooks", Repos: []string{"../proj--*"}}
	if errs := cfg.Validate(); len(errs) != 0 {
		t.Errorf("valid git_hooks rejected: %v", errs)
	}

	cfg = base()
	cfg.Settings.GitHooks = config.GitHooksSettings{Repos: []string{"../proj--*"}}
	if errs := cfg.Validate(); len(errs) != 1 {
		t.Errorf("repos without dir: expected 1 error, got: %v", errs)
	}

	cfg = base()
	cfg.Settings.GitHooks = config.GitHooksSettings{Dir: ".apiary/hooks"}
	if errs := cfg.Validate(); len(errs) != 1 {
		t.Errorf("dir without repos: expected 1 error, got: %v", errs)
	}

	cfg = base()
	cfg.Settings.GitHooks = config.GitHooksSettings{Dir: ".apiary/hooks", Repos: []string{"  "}}
	if errs := cfg.Validate(); len(errs) != 1 {
		t.Errorf("blank repo pattern: expected 1 error, got: %v", errs)
	}
}
