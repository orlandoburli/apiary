package editor_test

import (
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/editor"
)

// ── serializer ──────────────────────────────────────────────────────────────

func TestWorkflowToYAML_roundtrip(t *testing.T) {
	wf := config.WorkflowConfig{
		ID:          "my-wf",
		Description: "Test workflow",
		Steps: []config.StepConfig{
			{ID: "step1", Agent: "agent-a", Prompt: "Do something"},
			{ID: "step2", Agent: "agent-b"},
		},
	}

	yamlStr, err := editor.WorkflowToYAML(wf)
	if err != nil {
		t.Fatalf("WorkflowToYAML: %v", err)
	}
	if !strings.Contains(yamlStr, "id: my-wf") {
		t.Errorf("expected 'id: my-wf' in YAML, got:\n%s", yamlStr)
	}
	if !strings.Contains(yamlStr, "step1") {
		t.Errorf("expected step1 in YAML")
	}
}

func TestReplaceWorkflowInRaw_replaces_and_preserves(t *testing.T) {
	raw := `version: "1"
agents:
  - id: agent-a
    model: claude-sonnet-4-6
workflows:
  - id: wf-one
    description: First
    steps:
      - id: step1
        agent: agent-a
  - id: wf-two
    description: Second
    steps:
      - id: step2
        agent: agent-a
`

	wf := config.WorkflowConfig{
		ID:          "wf-one",
		Description: "First modified",
		Steps: []config.StepConfig{
			{ID: "step1", Agent: "agent-a", Prompt: "Updated prompt"},
		},
	}

	updated, err := editor.ReplaceWorkflowInRaw(raw, "wf-one", wf)
	if err != nil {
		t.Fatalf("ReplaceWorkflowInRaw: %v", err)
	}

	// The other workflow must still be present.
	if !strings.Contains(updated, "id: wf-two") {
		t.Error("wf-two missing after replace")
	}
	// The agents section must be preserved.
	if !strings.Contains(updated, "id: agent-a") {
		t.Error("agents section missing after replace")
	}
	// The modified description must appear.
	if !strings.Contains(updated, "First modified") {
		t.Errorf("modified description missing; updated:\n%s", updated)
	}
}

func TestReplaceWorkflowInRaw_single_workflow(t *testing.T) {
	raw := `version: "1"
workflows:
  - id: only-wf
    steps:
      - id: s1
        agent: a1
`
	wf := config.WorkflowConfig{
		ID: "only-wf",
		Steps: []config.StepConfig{
			{ID: "s1", Agent: "a1"},
			{ID: "s2", Agent: "a2"},
		},
	}
	updated, err := editor.ReplaceWorkflowInRaw(raw, "only-wf", wf)
	if err != nil {
		t.Fatalf("ReplaceWorkflowInRaw: %v", err)
	}
	if !strings.Contains(updated, "id: s2") {
		t.Errorf("newly added step s2 missing in:\n%s", updated)
	}
}

func TestReplaceWorkflowInRaw_not_found_returns_error(t *testing.T) {
	raw := `version: "1"
workflows:
  - id: wf-a
    steps: []
`
	_, err := editor.ReplaceWorkflowInRaw(raw, "wf-missing", config.WorkflowConfig{})
	if err == nil {
		t.Error("expected error for missing workflow ID, got nil")
	}
	// Verify the error message is actionable (not a fallback-marshal error).
	if err != nil && !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

// TestReplaceWorkflowInRaw_quoted_id verifies that workflow IDs written with
// double or single quotes in the YAML file are found and replaced correctly.
func TestReplaceWorkflowInRaw_quoted_id(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{
			name: "double-quoted id",
			raw: `version: "1"
workflows:
  - id: "my-wf"
    steps:
      - id: s1
        agent: a1
`,
		},
		{
			name: "single-quoted id",
			raw: `version: "1"
workflows:
  - id: 'my-wf'
    steps:
      - id: s1
        agent: a1
`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wf := config.WorkflowConfig{
				ID: "my-wf",
				Steps: []config.StepConfig{
					{ID: "s1", Agent: "a1", Prompt: "updated"},
				},
			}
			updated, err := editor.ReplaceWorkflowInRaw(tc.raw, "my-wf", wf)
			if err != nil {
				t.Fatalf("ReplaceWorkflowInRaw(%s): %v", tc.name, err)
			}
			if !strings.Contains(updated, "updated") {
				t.Errorf("updated prompt missing from output:\n%s", updated)
			}
		})
	}
}

func TestExtractWorkflowBlock(t *testing.T) {
	raw := `workflows:
  - id: alpha
    steps:
      - id: s1
  - id: beta
    steps:
      - id: s2
`
	block := editor.ExtractWorkflowBlock(raw, "alpha")
	if !strings.Contains(block, "alpha") {
		t.Errorf("expected alpha in block, got: %q", block)
	}
	if strings.Contains(block, "beta") {
		t.Errorf("beta should not appear in alpha block")
	}
}

// ── diff ────────────────────────────────────────────────────────────────────

func TestComputeDiff_no_changes(t *testing.T) {
	text := "id: wf\nsteps:\n  - id: s1\n"
	lines := editor.ComputeDiff(text, text)
	if editor.DiffHasChanges(lines) {
		t.Error("identical texts should produce no diff changes")
	}
}

func TestComputeDiff_addition(t *testing.T) {
	original := "line1\nline2\n"
	updated := "line1\nline2\nline3\n"
	lines := editor.ComputeDiff(original, updated)
	if !editor.DiffHasChanges(lines) {
		t.Error("expected diff changes for added line")
	}
	found := false
	for _, l := range lines {
		if l.Kind == editor.DiffAdded && strings.Contains(l.Text, "line3") {
			found = true
		}
	}
	if !found {
		t.Error("added line3 not found in diff")
	}
}

func TestComputeDiff_removal(t *testing.T) {
	original := "a\nb\nc\n"
	updated := "a\nc\n"
	lines := editor.ComputeDiff(original, updated)
	if !editor.DiffHasChanges(lines) {
		t.Error("expected diff changes for removed line")
	}
	found := false
	for _, l := range lines {
		if l.Kind == editor.DiffRemoved && l.Text == "b" {
			found = true
		}
	}
	if !found {
		t.Error("removed line 'b' not found in diff")
	}
}

// ── unsupported detection ───────────────────────────────────────────────────

func TestDetectUnsupported_clean(t *testing.T) {
	raw := "workflows:\n  - id: wf\n    steps:\n      - id: s1\n        agent: a1\n"
	wf := config.WorkflowConfig{ID: "wf", Steps: []config.StepConfig{{ID: "s1", Agent: "a1"}}}
	updated, err := editor.ReplaceWorkflowInRaw(raw, "wf", wf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(updated, "&") || strings.Contains(updated, " *") {
		t.Error("clean output should not contain YAML anchors or aliases")
	}
}

// TestReadOnlySteps_populated verifies that buildReadOnlySteps (exercised via
// New) correctly marks steps whose raw YAML contains anchors or aliases as
// read-only, while leaving unaffected steps editable.
func TestReadOnlySteps_populated(t *testing.T) {
	raw := `version: "1"
workflows:
  - id: wf
    steps:
      - id: anchored-step
        agent: &my-anchor agent-a
      - id: normal-step
        agent: agent-b
`
	cfg := &config.Config{
		Workflows: []config.WorkflowConfig{
			{
				ID: "wf",
				Steps: []config.StepConfig{
					{ID: "anchored-step", Agent: "agent-a"},
					{ID: "normal-step", Agent: "agent-b"},
				},
			},
		},
	}

	m := editor.New(cfg, "apiary.yaml", 0, raw)

	if !m.ReadOnlyStep(0) {
		t.Error("step 0 (anchored-step) should be read-only due to YAML anchor")
	}
	if m.ReadOnlyStep(1) {
		t.Error("step 1 (normal-step) should NOT be read-only")
	}
}

// TestSave_no_fallback_on_missing_workflow ensures that save() surfaces an
// error when the workflow ID cannot be found in the raw file, rather than
// silently falling back to yaml.Marshal which would expand ${VAR} secrets.
func TestSave_no_fallback_on_missing_workflow(t *testing.T) {
	// ReplaceWorkflowInRaw with a non-existent workflow ID must return an error.
	raw := `version: "1"
workflows:
  - id: other-wf
    steps: []
`
	_, err := editor.ReplaceWorkflowInRaw(raw, "ghost-wf", config.WorkflowConfig{ID: "ghost-wf"})
	if err == nil {
		t.Fatal("expected error when workflow ID not found, got nil — secret-leak fallback may still be present")
	}
}

// ── form apply ──────────────────────────────────────────────────────────────

func TestForm_apply_agent_step(t *testing.T) {
	step := config.StepConfig{
		ID:    "classify",
		Agent: "old-agent",
	}
	f := editor.NewStepForm(step)

	// Simulate typing a new agent name into the Agent field.
	for _, field := range f.Fields {
		if field.Key == "agent" {
			// Set the value directly (simulating user input already in field).
			break
		}
	}
	// Set the field value via the form's field list directly.
	for i, field := range f.Fields {
		if field.Key == "agent" {
			f.Fields[i].Value = "new-agent"
			break
		}
	}

	applied := f.Apply()
	if applied.Agent != "new-agent" {
		t.Errorf("expected agent 'new-agent', got %q", applied.Agent)
	}
	if applied.ID != "classify" {
		t.Errorf("ID should be preserved, got %q", applied.ID)
	}
}

func TestForm_apply_on_fail(t *testing.T) {
	step := config.StepConfig{ID: "impl"}
	f := editor.NewStepForm(step)

	for i, field := range f.Fields {
		switch field.Key {
		case "on_fail_goto":
			f.Fields[i].Value = "impl"
		case "on_fail_retries":
			f.Fields[i].Value = "3"
		}
	}

	applied := f.Apply()
	if applied.OnFail == nil {
		t.Fatal("OnFail should be set")
	}
	if applied.OnFail.Goto != "impl" {
		t.Errorf("on_fail.goto = %q, want 'impl'", applied.OnFail.Goto)
	}
	if applied.OnFail.MaxRetries != 3 {
		t.Errorf("on_fail.max_retries = %d, want 3", applied.OnFail.MaxRetries)
	}
}

func TestForm_apply_trigger(t *testing.T) {
	f := editor.NewTriggerForm(nil)

	for i, field := range f.Fields {
		switch field.Key {
		case "source":
			f.Fields[i].Value = "github"
		case "labels":
			f.Fields[i].Value = "ai-ready,enhancement"
		case "priority":
			f.Fields[i].Value = "5"
		}
	}

	trig := &config.TriggerConfig{}
	out := f.ApplyTrigger(trig)
	if out.Match.Source != "github" {
		t.Errorf("source = %q, want 'github'", out.Match.Source)
	}
	if len(out.Match.Labels) != 2 {
		t.Errorf("labels = %v, want 2 items", out.Match.Labels)
	}
	if out.Priority != 5 {
		t.Errorf("priority = %d, want 5", out.Priority)
	}
}
