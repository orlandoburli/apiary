package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveEnvVarPreserved(t *testing.T) {
	dir := t.TempDir()
	yaml := `version: "1"
sources:
  - id: r
    type: github
    config:
      api_key: ${GITHUB_TOKEN}
agents:
  - id: eng
    model: sonnet
`
	path := filepath.Join(dir, "apiary.yaml")
	os.WriteFile(path, []byte(yaml), 0644)

	os.Setenv("GITHUB_TOKEN", "ghp_secret")
	defer os.Unsetenv("GITHUB_TOKEN")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	cfg.Agents[0].MaxWorkers = 5
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}

	saved, _ := os.ReadFile(path)
	body := string(saved)

	if !strings.Contains(body, "${GITHUB_TOKEN}") {
		t.Errorf("${GITHUB_TOKEN} was expanded:\n%s", body)
	}
	if !strings.Contains(body, "max_workers: 5") {
		t.Errorf("max_workers: 5 missing:\n%s", body)
	}
}

func TestSaveApplyAgentDiffRunner(t *testing.T) {
	dir := t.TempDir()
	yaml := `version: "1"
agents:
  - id: eng
    model: sonnet
    max_workers: 2
`
	path := filepath.Join(dir, "apiary.yaml")
	os.WriteFile(path, []byte(yaml), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	diff := AgentDiff{ID: "eng", Runner: "opencode", MaxWorkers: 5}
	if err := cfg.ApplyAgentDiff(path, diff); err != nil {
		t.Fatal(err)
	}

	if cfg.Agents[0].Runner != "opencode" {
		t.Errorf("Runner = %q, want opencode", cfg.Agents[0].Runner)
	}
	if cfg.Agents[0].MaxWorkers != 5 {
		t.Errorf("MaxWorkers = %d, want 5", cfg.Agents[0].MaxWorkers)
	}

	saved, _ := os.ReadFile(path)
	body := string(saved)

	if !strings.Contains(body, "runner: opencode") {
		t.Errorf("runner missing:\n%s", body)
	}
	if !strings.Contains(body, "max_workers: 5") {
		t.Errorf("max_workers:5 missing:\n%s", body)
	}
}

func TestSaveApplyAgentDiffModel(t *testing.T) {
	dir := t.TempDir()
	yaml := `version: "1"
agents:
  - id: eng
    model: claude-sonnet-4-6
`
	path := filepath.Join(dir, "apiary.yaml")
	os.WriteFile(path, []byte(yaml), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	diff := AgentDiff{ID: "eng", Model: "claude-opus-4-8"}
	if err := cfg.ApplyAgentDiff(path, diff); err != nil {
		t.Fatal(err)
	}

	if cfg.Agents[0].Model != "claude-opus-4-8" {
		t.Errorf("Model = %q, want claude-opus-4-8", cfg.Agents[0].Model)
	}

	saved, _ := os.ReadFile(path)
	body := string(saved)

	if !strings.Contains(body, "model: claude-opus-4-8") {
		t.Errorf("model not updated:\n%s", body)
	}
	if strings.Contains(body, "claude-sonnet") {
		t.Errorf("old model still present:\n%s", body)
	}
}

func TestSaveApplyAgentDiffMemoryOnly(t *testing.T) {
	cfg := &Config{
		Agents: []AgentConfig{
			{ID: "eng", Model: "sonnet", MaxWorkers: 2},
		},
	}

	diff := AgentDiff{ID: "eng", Runner: "claude-cli", MaxWorkers: 5}
	if err := cfg.ApplyAgentDiff("", diff); err != nil {
		t.Fatal(err)
	}

	if cfg.Agents[0].Runner != "claude-cli" {
		t.Errorf("Runner = %q, want claude-cli", cfg.Agents[0].Runner)
	}
	if cfg.Agents[0].MaxWorkers != 5 {
		t.Errorf("MaxWorkers = %d, want 5", cfg.Agents[0].MaxWorkers)
	}
}

func TestSavePreservesFormatting(t *testing.T) {
	dir := t.TempDir()
	yaml := `version: "1"
agents:
  - id: eng
    description: implements
    model: claude-sonnet-4-6
    max_workers: 2

  - id: rev
    description: reviews
    max_workers: 1
`
	path := filepath.Join(dir, "apiary.yaml")
	os.WriteFile(path, []byte(yaml), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	cfg.Agents[0].Runner = "opencode-cli"
	cfg.Agents[0].MaxWorkers = 5
	cfg.Agents[1].MaxWorkers = 3

	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}

	saved, _ := os.ReadFile(path)
	body := string(saved)

	if !strings.Contains(body, "runner: opencode-cli") {
		t.Errorf("runner missing:\n%s", body)
	}
	if !strings.Contains(body, "max_workers: 5") {
		t.Errorf("max_workers:5 missing:\n%s", body)
	}
	if !strings.Contains(body, "max_workers: 3") {
		t.Errorf("max_workers:3 for rev missing:\n%s", body)
	}
	if !strings.Contains(body, "    description: implements") {
		t.Errorf("original indent lost:\n%s", body)
	}
}

// TestSaveNoRawContentReturnsError verifies that Save() refuses to write when
// the config was not loaded from a file, preventing yaml.Marshal from
// emitting expanded ${VAR} secrets.
func TestSaveNoRawContentReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "apiary.yaml")

	cfg := &Config{
		Agents: []AgentConfig{{ID: "eng", Model: "sonnet"}},
	}
	err := cfg.Save(path)
	if err == nil {
		t.Fatal("expected error when saving a config without rawContent, got nil")
	}
	if !strings.Contains(err.Error(), "rawContent is empty") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestSaveQuotedAgentID verifies that ApplyAgentDiff correctly locates an
// agent whose YAML id is double-quoted (e.g. - id: "my-agent").
func TestSaveQuotedAgentID(t *testing.T) {
	dir := t.TempDir()
	yaml := `version: "1"
agents:
  - id: "eng"
    model: sonnet
`
	path := filepath.Join(dir, "apiary.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	diff := AgentDiff{ID: "eng", Model: "claude-opus-4-8"}
	if err := cfg.ApplyAgentDiff(path, diff); err != nil {
		t.Fatal(err)
	}

	saved, _ := os.ReadFile(path)
	if !strings.Contains(string(saved), "model: claude-opus-4-8") {
		t.Errorf("model not updated for quoted id:\n%s", saved)
	}
}

// TestInsertLineNoBacking verifies that insertLine never returns a slice
// sharing backing memory with the original, preventing silent aliasing.
func TestInsertLineNoBacking(t *testing.T) {
	orig := make([]string, 3, 10) // extra capacity to expose aliasing bugs
	orig[0], orig[1], orig[2] = "a", "b", "c"
	result := insertLine(orig, 1, "x")

	if len(result) != 4 {
		t.Fatalf("expected length 4, got %d", len(result))
	}
	if result[0] != "a" || result[1] != "x" || result[2] != "b" || result[3] != "c" {
		t.Errorf("unexpected result: %v", result)
	}
	// Mutate orig to confirm no shared backing.
	orig[1] = "MUTATED"
	if result[2] == "MUTATED" {
		t.Error("insertLine shares backing array with original slice")
	}
}
