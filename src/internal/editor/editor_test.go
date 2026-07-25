package editor

import (
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
)

// sampleCfg returns a minimal Config for testing.
func sampleCfg() *config.Config {
	t := true
	return &config.Config{
		Version: "1",
		Runners: []config.RunnerConfig{{ID: "r1", Type: "cli", Provider: "claude"}},
		Sources: []config.SourceConfig{{ID: "src1", Type: "github"}},
		Agents:  []config.AgentConfig{{ID: "agent1", Model: "claude-sonnet-4-6"}},
		Workflows: []config.WorkflowConfig{
			{
				ID:          "test-wf",
				Description: "Test workflow",
				Trigger: &config.TriggerConfig{
					Priority: 10,
					Match:    config.RouteMatch{Source: "src1", Labels: []string{"ai-ready"}},
				},
				Steps: []config.StepConfig{
					{
						ID:    "classify",
						Agent: "agent1",
						Prompt: "Classify the issue.",
						OnFail: &config.StepOutcome{Goto: "classify", MaxRetries: 2},
					},
					{
						ID:   "route",
						Type: config.StepTypeSplit,
						Branches: []config.SplitBranch{
							{If: `memory.track == "complex"`, Goto: "implement"},
							{Else: true, Goto: "quick-fix"},
						},
					},
					{ID: "implement", Agent: "agent1", Prompt: "Implement the change."},
					{ID: "quick-fix", Agent: "agent1", Prompt: "Apply the quick fix."},
					{
						ID:   "wait-ci",
						Type: config.StepTypeWaitFor,
						WaitFor: &config.WaitForConfig{
							Kind:            config.WaitKindCI,
							CheckInterval:   "60s",
							MaxDuration:     "2h",
							FailIfNotPassed: &t,
						},
					},
				},
				OnComplete: &config.OnComplete{SetState: "closed"},
				OnFail:     &config.OnComplete{AddLabels: []string{"needs-attention"}},
			},
		},
	}
}

// TestRoundTrip verifies that a supported workflow survives a
// configToEditor → editorToWorkflow round-trip without semantic changes.
func TestRoundTrip(t *testing.T) {
	orig := sampleCfg()
	origWF := orig.Workflows[0]

	ec := configToEditor(orig, nil, "apiary.yaml")
	if len(ec.Workflows) != 1 {
		t.Fatalf("expected 1 workflow, got %d", len(ec.Workflows))
	}

	back := editorToWorkflow(ec.Workflows[0])

	// ID / Description
	if back.ID != origWF.ID {
		t.Errorf("ID: got %q, want %q", back.ID, origWF.ID)
	}
	if back.Description != origWF.Description {
		t.Errorf("Description: got %q, want %q", back.Description, origWF.Description)
	}

	// Trigger
	if back.Trigger == nil {
		t.Fatal("Trigger is nil after round-trip")
	}
	if back.Trigger.Priority != origWF.Trigger.Priority {
		t.Errorf("Trigger.Priority: got %d, want %d", back.Trigger.Priority, origWF.Trigger.Priority)
	}
	if back.Trigger.Match.Source != origWF.Trigger.Match.Source {
		t.Errorf("Trigger.Match.Source: got %q, want %q", back.Trigger.Match.Source, origWF.Trigger.Match.Source)
	}

	// Steps
	if len(back.Steps) != len(origWF.Steps) {
		t.Fatalf("Steps len: got %d, want %d", len(back.Steps), len(origWF.Steps))
	}

	// classify step
	cs := back.Steps[0]
	if cs.ID != "classify" {
		t.Errorf("Steps[0].ID: got %q, want classify", cs.ID)
	}
	if cs.Agent != "agent1" {
		t.Errorf("Steps[0].Agent: got %q, want agent1", cs.Agent)
	}
	if cs.OnFail == nil || cs.OnFail.Goto != "classify" {
		t.Errorf("Steps[0].OnFail.Goto: got %v, want classify", cs.OnFail)
	}
	if cs.OnFail.MaxRetries != 2 {
		t.Errorf("Steps[0].OnFail.MaxRetries: got %d, want 2", cs.OnFail.MaxRetries)
	}

	// split step
	rs := back.Steps[1]
	if rs.Type != config.StepTypeSplit {
		t.Errorf("Steps[1].Type: got %q, want split", rs.Type)
	}
	if len(rs.Branches) != 2 {
		t.Errorf("Steps[1].Branches len: got %d, want 2", len(rs.Branches))
	}
	if rs.Branches[1].Else != true {
		t.Errorf("Steps[1].Branches[1].Else: want true")
	}
	if rs.Branches[1].Goto != "quick-fix" {
		t.Errorf("Steps[1].Branches[1].Goto: got %q, want quick-fix", rs.Branches[1].Goto)
	}

	// wait_for step
	ws := back.Steps[4]
	if ws.Type != config.StepTypeWaitFor {
		t.Errorf("Steps[4].Type: got %q, want wait_for", ws.Type)
	}
	if ws.WaitFor == nil {
		t.Fatal("Steps[4].WaitFor is nil")
	}
	if ws.WaitFor.Kind != "ci" {
		t.Errorf("Steps[4].WaitFor.Kind: got %q, want ci", ws.WaitFor.Kind)
	}
	if ws.WaitFor.CheckInterval != "60s" {
		t.Errorf("Steps[4].WaitFor.CheckInterval: got %q, want 60s", ws.WaitFor.CheckInterval)
	}

	// on_complete
	if back.OnComplete == nil || back.OnComplete.SetState != "closed" {
		t.Errorf("OnComplete.SetState: want closed, got %v", back.OnComplete)
	}
}

// TestSupportedFlag verifies the supported/unsupported tagging of steps.
func TestSupportedFlag(t *testing.T) {
	cfg := sampleCfg()
	// Add a parallel step (unsupported in the visual editor).
	cfg.Workflows[0].Steps = append(cfg.Workflows[0].Steps, config.StepConfig{
		ID:   "par",
		Type: config.StepTypeParallel,
		SubSteps: []config.StepConfig{
			{ID: "a", Agent: "agent1"},
			{ID: "b", Agent: "agent1"},
		},
	})

	ec := configToEditor(cfg, nil, "apiary.yaml")
	wf := ec.Workflows[0]

	// All non-parallel steps should be supported.
	for i, s := range wf.Steps[:5] {
		if !s.Supported {
			t.Errorf("Steps[%d] (%s): expected supported=true", i, s.ID)
		}
	}
	// The parallel step should be unsupported.
	par := wf.Steps[5]
	if par.Supported {
		t.Errorf("parallel step: expected supported=false")
	}
	if !wf.HasUnsupported {
		t.Error("HasUnsupported should be true when a parallel step is present")
	}
}

// TestUnifiedDiff verifies the diff algorithm produces valid output.
func TestUnifiedDiff(t *testing.T) {
	a := "line1\nline2\nline3\n"
	b := "line1\nlineX\nline3\n"
	d := unifiedDiff(a, b)

	if !strings.Contains(d, "-line2") {
		t.Errorf("diff should contain removed line2, got:\n%s", d)
	}
	if !strings.Contains(d, "+lineX") {
		t.Errorf("diff should contain added lineX, got:\n%s", d)
	}
}

// TestNoDiff verifies identical inputs produce the no-changes sentinel.
func TestNoDiff(t *testing.T) {
	same := "a\nb\nc\n"
	d := unifiedDiff(same, same)
	if d != "(no semantic changes)" {
		t.Errorf("identical inputs should produce no-diff sentinel, got: %q", d)
	}
}

// TestParseErrorPath verifies extraction of workflow/step IDs from error messages.
func TestParseErrorPath(t *testing.T) {
	msg := `workflows[0] "triage": steps[1] "classify": agent "investigator" not defined`
	wid, sid := parseErrorPath(msg)
	if wid != "triage" {
		t.Errorf("workflowID: got %q, want triage", wid)
	}
	if sid != "classify" {
		t.Errorf("stepID: got %q, want classify", sid)
	}
}

// anchorYAML is a minimal apiary.yaml that uses YAML anchors and aliases.
const anchorYAML = `version: "1"
agents:
  - id: eng
    model: sonnet
defaults: &defaults
  description: shared defaults
workflows:
  - id: anchored
    <<: *defaults
    steps:
      - id: do-it
        agent: eng
  - id: clean
    steps:
      - id: run
        agent: eng
`

// TestAnchorDetectionMarksWorkflowUnsupported verifies that a workflow whose
// YAML block contains an alias (*) is reported as HasUnsupported=true.
func TestAnchorDetectionMarksWorkflowUnsupported(t *testing.T) {
	// Build a minimal config whose workflow IDs match those in anchorYAML.
	cfg := &config.Config{
		Agents: []config.AgentConfig{{ID: "eng", Model: "sonnet"}},
		Workflows: []config.WorkflowConfig{
			{ID: "anchored", Steps: []config.StepConfig{{ID: "do-it", Agent: "eng"}}},
			{ID: "clean", Steps: []config.StepConfig{{ID: "run", Agent: "eng"}}},
		},
	}
	ec := configToEditor(cfg, []byte(anchorYAML), "apiary.yaml")

	var anchored, clean *EditorWorkflow
	for i := range ec.Workflows {
		switch ec.Workflows[i].ID {
		case "anchored":
			anchored = &ec.Workflows[i]
		case "clean":
			clean = &ec.Workflows[i]
		}
	}
	if anchored == nil || !anchored.HasUnsupported {
		t.Error("workflow with YAML alias should have HasUnsupported=true")
	}
	if clean != nil && clean.HasUnsupported {
		t.Error("clean workflow should have HasUnsupported=false")
	}
}

// TestReplaceWorkflowInTextPreservesEnvRefs verifies that replaceWorkflowInText
// keeps ${VAR} references in non-workflow sections intact (i.e. the function
// touches only the targeted workflow block).
func TestReplaceWorkflowInTextPreservesEnvRefs(t *testing.T) {
	raw := `version: "1"
sources:
  - id: gh
    type: github
    config:
      api_key: ${GITHUB_TOKEN}
workflows:
  - id: build
    steps:
      - id: compile
        agent: eng
`
	wf := config.WorkflowConfig{
		ID: "build",
		Steps: []config.StepConfig{
			{ID: "compile", Agent: "eng"},
			{ID: "test", Agent: "eng"},
		},
	}
	result, err := replaceWorkflowInText(raw, wf)
	if err != nil {
		t.Fatalf("replaceWorkflowInText error: %v", err)
	}
	if !strings.Contains(result, "${GITHUB_TOKEN}") {
		t.Errorf("${GITHUB_TOKEN} was lost after workflow replacement:\n%s", result)
	}
	if !strings.Contains(result, "id: test") {
		t.Errorf("new step 'test' not present after replacement:\n%s", result)
	}
}

// TestReplaceWorkflowInTextQuotedID verifies that replaceWorkflowInText finds
// a workflow block whose id is double-quoted in the raw YAML.
func TestReplaceWorkflowInTextQuotedID(t *testing.T) {
	raw := `version: "1"
workflows:
  - id: "my-flow"
    steps:
      - id: s1
        agent: eng
`
	wf := config.WorkflowConfig{
		ID:    "my-flow",
		Steps: []config.StepConfig{{ID: "s1", Agent: "eng"}, {ID: "s2", Agent: "eng"}},
	}
	result, err := replaceWorkflowInText(raw, wf)
	if err != nil {
		t.Fatalf("replaceWorkflowInText error: %v", err)
	}
	if !strings.Contains(result, "id: s2") {
		t.Errorf("step s2 not present after replacing quoted-id workflow:\n%s", result)
	}
}

// TestScanAnchoredWorkflows verifies that scanAnchoredWorkflows correctly
// identifies workflows that contain YAML anchors or aliases.
func TestScanAnchoredWorkflows(t *testing.T) {
	anchored := scanAnchoredWorkflows([]byte(anchorYAML))
	if !anchored["anchored"] {
		t.Error("expected 'anchored' workflow to be detected as anchored")
	}
	if anchored["clean"] {
		t.Error("expected 'clean' workflow to not be detected as anchored")
	}
}
