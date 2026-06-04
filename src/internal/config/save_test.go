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
