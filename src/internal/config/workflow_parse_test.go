package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWorkflowYAMLParsing verifies that a representative workflows: block in
// apiary.yaml unmarshals into the expected struct shape (all step types, memory,
// output_schema, split branches with else: true) and passes validation.
func TestWorkflowYAMLParsing(t *testing.T) {
	dir := t.TempDir()
	yaml := `version: "1"
sources:
  - id: main-plane
    type: plane
agents:
  - id: architect
    model: claude-opus-4-8
  - id: backend-dev
    model: claude-sonnet-4-6
workflows:
  - id: feature-development
    description: "Full feature pipeline"
    resume: allowed
    result_comment: on_complete
    trigger:
      priority: 10
      match:
        source: main-plane
        labels: [feature, ai-ready]
    steps:
      - id: plan
        agent: architect
        model: claude-opus-4-8
        summary_prompt: "Summarize the plan."
        output_schema:
          type: object
          properties:
            complexity: {type: string, enum: [low, medium, high]}
            issues:
              type: array
              items:
                type: object
                properties:
                  file: {type: string}
          required: [complexity]
        memory:
          read: true
          write: [complexity]
      - id: route
        type: split
        branches:
          - if: "memory.complexity == 'high'"
            goto: implement
          - else: true
            goto: implement
      - id: implement
        agent: backend-dev
        memory:
          read: false
      - id: review
        agent: backend-dev
        on_fail:
          goto: implement
          max_retries: 2
      - id: fix-each
        type: foreach
        items: "steps.plan.output.issues"
        as: issue
        concurrency: 4
        max_items: 20
        step:
          agent: backend-dev
      - id: human-approval
        type: approval
        message: "Approve?"
        resume_on:
          comment_contains: "approve"
        abort_on:
          comment_contains: "reject"
        timeout: 48h
    on_complete:
      set_state: in_review
    on_fail:
      set_state: blocked
      add_labels: [workflow-failed]
`
	path := filepath.Join(dir, "apiary.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(cfg.Workflows) != 1 {
		t.Fatalf("expected 1 workflow, got %d", len(cfg.Workflows))
	}
	wf := cfg.Workflows[0]

	if wf.ID != "feature-development" {
		t.Errorf("workflow id = %q", wf.ID)
	}
	if wf.ResumePolicy() != ResumeAllowed {
		t.Errorf("resume policy = %q", wf.ResumePolicy())
	}
	if wf.Trigger == nil || wf.Trigger.Priority != 10 || wf.Trigger.Match.Source != "main-plane" {
		t.Errorf("trigger parsed incorrectly: %+v", wf.Trigger)
	}
	if len(wf.Steps) != 6 {
		t.Fatalf("expected 6 steps, got %d", len(wf.Steps))
	}

	// plan: agent step with output_schema + memory.write
	plan := wf.Steps[0]
	if plan.StepType() != StepTypeAgent {
		t.Errorf("plan type = %q", plan.StepType())
	}
	if plan.Model != "claude-opus-4-8" {
		t.Errorf("plan model override = %q", plan.Model)
	}
	if plan.OutputSchema == nil || plan.OutputSchema.Properties["complexity"].Type != "string" {
		t.Errorf("plan output_schema parsed incorrectly: %+v", plan.OutputSchema)
	}
	if len(plan.OutputSchema.Properties["complexity"].Enum) != 3 {
		t.Errorf("plan complexity enum = %v", plan.OutputSchema.Properties["complexity"].Enum)
	}
	issues := plan.OutputSchema.Properties["issues"]
	if issues.Type != "array" || issues.Items == nil || issues.Items.Type != "object" {
		t.Errorf("plan issues array parsed incorrectly: %+v", issues)
	}
	if !plan.MemoryReadEnabled() {
		t.Error("plan memory.read should default/parse to true")
	}
	if got := plan.MemoryWriteFields(); len(got) != 1 || got[0] != "complexity" {
		t.Errorf("plan memory.write = %v", got)
	}

	// route: split step, second branch is the fallback
	route := wf.Steps[1]
	if route.StepType() != StepTypeSplit {
		t.Errorf("route type = %q", route.StepType())
	}
	if len(route.Branches) != 2 || !route.Branches[1].IsFallback() {
		t.Errorf("route branches parsed incorrectly: %+v", route.Branches)
	}
	if route.Branches[0].IsFallback() {
		t.Error("first branch should not be a fallback")
	}

	// implement: memory.read explicitly false
	implement := wf.Steps[2]
	if implement.MemoryReadEnabled() {
		t.Error("implement memory.read should parse to false")
	}

	// review: on_fail back-edge
	review := wf.Steps[3]
	if review.OnFail == nil || review.OnFail.Goto != "implement" || review.OnFail.MaxRetries != 2 {
		t.Errorf("review on_fail parsed incorrectly: %+v", review.OnFail)
	}

	// fix-each: foreach with inner step
	fe := wf.Steps[4]
	if fe.StepType() != StepTypeForeach || fe.Items != "steps.plan.output.issues" || fe.Step == nil {
		t.Errorf("fix-each parsed incorrectly: %+v", fe)
	}
	if fe.Step.Agent != "backend-dev" {
		t.Errorf("fix-each inner agent = %q", fe.Step.Agent)
	}

	// human-approval: approval step
	ap := wf.Steps[5]
	if ap.StepType() != StepTypeApproval || ap.ResumeOn == nil || ap.ResumeOn.CommentContains != "approve" {
		t.Errorf("approval parsed incorrectly: %+v", ap)
	}
	if ap.ParsedTimeout().Hours() != 48 {
		t.Errorf("approval timeout = %v", ap.ParsedTimeout())
	}

	// on_complete / on_fail hooks
	if wf.OnComplete == nil || wf.OnComplete.SetState != "in_review" {
		t.Errorf("on_complete parsed incorrectly: %+v", wf.OnComplete)
	}
	if wf.OnFail == nil || wf.OnFail.SetState != "blocked" || len(wf.OnFail.AddLabels) != 1 {
		t.Errorf("on_fail parsed incorrectly: %+v", wf.OnFail)
	}

	// And the whole thing must validate cleanly.
	if errs := cfg.Validate(); len(errs) != 0 {
		t.Fatalf("expected valid config, got errors: %v", errs)
	}
}
