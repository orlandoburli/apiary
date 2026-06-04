package config_test

import (
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
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
		Routes: []config.RouteConfig{
			{ID: "r-1", Priority: 1, Agent: "a-1",
				Match: config.RouteMatch{Source: "src-1"}},
		},
	}
	if errs := cfg.Validate(); len(errs) != 0 {
		t.Errorf("expected no errors, got: %v", errs)
	}
}

func TestValidate_MissingVersion(t *testing.T) {
	cfg := &config.Config{}
	assertError(t, cfg, "version is required")
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

func TestValidate_RouteReferencesUnknownAgent(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Sources: []config.SourceConfig{{ID: "src-1", Type: "plane"}},
		Agents: []config.AgentConfig{
			{ID: "a-1", Model: "claude-sonnet-4-6"},
		},
		Routes: []config.RouteConfig{
			{ID: "r-1", Priority: 1, Agent: "nonexistent",
				Match: config.RouteMatch{Source: "src-1"}},
		},
	}
	assertError(t, cfg, "not defined")
}

func TestValidate_RouteReferencesUnknownSource(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Sources: []config.SourceConfig{{ID: "src-1", Type: "plane"}},
		Workers: []config.WorkerConfig{{ID: "w-1", Runner: "cli", Model: "x"}},
		Routes: []config.RouteConfig{
			{ID: "r-1", Priority: 1, Worker: "w-1",
				Match: config.RouteMatch{Source: "nonexistent"}},
		},
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
