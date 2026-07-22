package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadResolvesLocalTypedSubworkflow(t *testing.T) {
	dir := t.TempDir()
	workflows := filepath.Join(dir, "workflows")
	if err := os.Mkdir(workflows, 0o755); err != nil {
		t.Fatal(err)
	}
	child := `id: prepare-repository
inputs:
  repository: {type: string, required: true}
outputs:
  workspace: {type: string, value: "${{ steps.checkout.workspace }}"}
steps:
  - id: checkout
    agent: engineer
    output:
      type: object
      properties:
        workspace: {type: string}
`
	if err := os.WriteFile(filepath.Join(workflows, "prepare.yaml"), []byte(child), 0o600); err != nil {
		t.Fatal(err)
	}
	main := `version: "1"
runners:
  - id: local
    type: script
    config: {}
default_runner: local
agents:
  - id: engineer
    runner: local
    model: test
workflows:
  - id: main
    steps:
      - id: prepare
        uses: ./workflows/prepare
        with:
          repository: "${{ task.repository }}"
`
	path := filepath.Join(dir, "apiary.yaml")
	if err := os.WriteFile(path, []byte(main), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Workflows) != 2 {
		t.Fatalf("workflows = %d, want 2", len(cfg.Workflows))
	}
	call := cfg.Workflows[0].Steps[0]
	if call.StepType() != StepTypeWorkflow || call.Workflow != "prepare-repository" {
		t.Fatalf("resolved call = %#v", call)
	}
	if cfg.Workflows[1].Inputs["repository"].Type != "string" {
		t.Fatalf("child input contract was not loaded: %#v", cfg.Workflows[1].Inputs)
	}
	if errs := cfg.Validate(); len(errs) > 0 {
		t.Fatalf("resolved config should validate: %v", errs)
	}
}

func TestLoadRejectsCyclicLocalSubworkflows(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("a.yaml", "id: a\nsteps:\n  - id: b\n    uses: ./b.yaml\n")
	write("b.yaml", "id: b\nsteps:\n  - id: a\n    uses: ./a.yaml\n")
	write("apiary.yaml", "version: \"1\"\nworkflows:\n  - id: main\n    steps:\n      - id: a\n        uses: ./a.yaml\n")

	_, err := Load(filepath.Join(dir, "apiary.yaml"))
	if err == nil || !strings.Contains(err.Error(), "cyclic subworkflow reference: a.yaml -> b.yaml -> a.yaml") {
		t.Fatalf("expected actionable cycle error, got %v", err)
	}
}

func TestLoadRejectsUnknownFieldInReusableWorkflow(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "child.yaml"), []byte("id: child\nunknown: true\nsteps: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	main := "version: \"1\"\nworkflows:\n  - id: main\n    steps:\n      - id: child\n        uses: ./child.yaml\n"
	path := filepath.Join(dir, "apiary.yaml")
	if err := os.WriteFile(path, []byte(main), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "field unknown not found") {
		t.Fatalf("expected strict child decode error, got %v", err)
	}
}
