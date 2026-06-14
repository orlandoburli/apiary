package config

import (
	"fmt"
	"strings"
	"testing"
)

// stubLintExpr installs a stand-in for the workflow package's parser (the real
// one is injected by the cli package): it rejects C-style operators, which is
// the failure mode from #180.
func stubLintExpr(t *testing.T) {
	t.Helper()
	prev := LintExpr
	LintExpr = func(expr string) error {
		if strings.Contains(expr, "&&") || strings.Contains(expr, "||") {
			return fmt.Errorf("unsupported operator")
		}
		return nil
	}
	t.Cleanup(func() { LintExpr = prev })
}

// TestValidate_LintsV2IfExpression runs the full Validate() pipeline so the v2
// `if:` is lowered to `condition:` before the lint sees it.
func TestValidate_LintsV2IfExpression(t *testing.T) {
	stubLintExpr(t)
	c := minimalConfig()
	c.Workflows = []WorkflowConfig{{
		ID: "w",
		Steps: []StepConfig{
			{ID: "classify", Agent: "a"},
			{ID: "impl", Agent: "a", If: `${{ classify.track != "100x" && classify.track != "decomposed" }}`},
		},
	}}
	errs := c.Validate()
	if !anyError(errs, "condition") {
		t.Errorf("expected a condition lint error, got: %v", errs)
	}
}

func TestValidate_LintsFailWhen(t *testing.T) {
	stubLintExpr(t)
	c := minimalConfig()
	c.Workflows = []WorkflowConfig{{
		ID: "w",
		Steps: []StepConfig{
			{ID: "review", Agent: "a", FailWhen: `memory.verdict == "rejected" || memory.verdict == "needs_work"`},
		},
	}}
	errs := c.Validate()
	if !anyError(errs, "fail_when") {
		t.Errorf("expected a fail_when lint error, got: %v", errs)
	}
}

func TestValidate_LintsSplitBranchIf(t *testing.T) {
	stubLintExpr(t)
	c := minimalConfig()
	c.Workflows = []WorkflowConfig{{
		ID: "w",
		Steps: []StepConfig{
			{ID: "plan", Agent: "a"},
			{ID: "route", Type: StepTypeSplit, DependsOn: []string{"plan"}, Branches: []SplitBranch{
				{If: `memory.level == "high" && memory.size == "xl"`, Goto: "senior"},
				{Else: true, Goto: "junior"},
			}},
			{ID: "senior", Agent: "a"},
			{ID: "junior", Agent: "a"},
		},
	}}
	errs := c.Validate()
	if !anyError(errs, "branches[0]") {
		t.Errorf("expected a branch lint error, got: %v", errs)
	}
}

func TestValidate_LintsParallelJoin(t *testing.T) {
	stubLintExpr(t)
	c := minimalConfig()
	c.Workflows = []WorkflowConfig{{
		ID: "w",
		Steps: []StepConfig{
			{ID: "par", Type: StepTypeParallel,
				Join: `${{ steps.lint.state == 'passed' && steps.tests.state == 'passed' }}`,
				SubSteps: []StepConfig{
					{ID: "lint", Agent: "a"},
					{ID: "tests", Agent: "a"},
				}},
		},
	}}
	errs := c.Validate()
	if !anyError(errs, "join") {
		t.Errorf("expected a join lint error, got: %v", errs)
	}
}

// TestValidate_LintSkippedWhenNil mirrors the KnownAdapters contract: configs
// built in code (LintExpr == nil) skip the expression lint.
func TestValidate_LintSkippedWhenNil(t *testing.T) {
	prev := LintExpr
	LintExpr = nil
	t.Cleanup(func() { LintExpr = prev })

	c := minimalConfig()
	c.Workflows = []WorkflowConfig{{
		ID: "w",
		Steps: []StepConfig{
			{ID: "impl", Agent: "a", Condition: `memory.a != "x" && memory.b != "y"`},
		},
	}}
	if errs := c.Validate(); len(errs) != 0 {
		t.Errorf("expected no errors with LintExpr nil, got: %v", errs)
	}
}
