package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
)

// prLinkWorkflow: one agent step that reports the PR it opened.
func prLinkWorkflow(field string) config.WorkflowConfig {
	return config.WorkflowConfig{ID: "impl", Steps: []config.StepConfig{
		{ID: "implement", Agent: "backend-dev", PullRequestFrom: field,
			OutputSchema: &config.OutputSchema{
				Type:       "object",
				Properties: map[string]config.SchemaField{"pr_url": {Type: "string"}},
			}},
	}}
}

// prLinkEngine wires a store with one binding for task c1.
func prLinkEngine(t *testing.T, exec StepExecutor, side SideEffects) (*Engine, *fakeStore) {
	t.Helper()
	store := newFakeStore()
	store.bindings["c1"] = []model.SourceBinding{{TaskID: "c1", SourceID: "jira", SourceItemID: "PSP-49"}}
	return testEngine(baseCfg(), store, exec, side), store
}

// The PR an agent reports is linked to the task, so PR-linked features work on
// a source that cannot enumerate pull requests itself (#425).
func TestPullRequestFrom_LinksReportedPR(t *testing.T) {
	exec := &fakeExecutor{results: map[string]StepResult{
		"implement": {Success: true, StructuredOutput: map[string]any{
			"pr_url": "https://github.com/acme/widgets/pull/42",
		}},
	}}
	side := &fakeSide{}
	eng, _ := prLinkEngine(t, exec, side)

	if _, _, err := eng.RunInstance(context.Background(), prLinkWorkflow("pr_url"), model.InternalTask{ID: "c1"}); err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(side.linkedPRs) != 1 {
		t.Fatalf("expected one linked PR, got %+v", side.linkedPRs)
	}
	if got := side.linkedPRs[0].Number; got != 42 {
		t.Errorf("PR number = %d, want 42", got)
	}
	if got := side.linkedPRs[0].URL; got != "https://github.com/acme/widgets/pull/42" {
		t.Errorf("PR url = %q", got)
	}
}

// A step that declares the mapping but reports no PR (nothing to do, no PR
// opened) links nothing and does not fail.
func TestPullRequestFrom_NoURLIsNoOp(t *testing.T) {
	exec := &fakeExecutor{results: map[string]StepResult{
		"implement": {Success: true, StructuredOutput: map[string]any{"pr_url": ""}},
	}}
	side := &fakeSide{}
	eng, _ := prLinkEngine(t, exec, side)

	_, success, _ := eng.RunInstance(context.Background(), prLinkWorkflow("pr_url"), model.InternalTask{ID: "c1"})
	if !success {
		t.Fatal("a step that opened no PR must still pass")
	}
	if len(side.linkedPRs) != 0 {
		t.Errorf("expected no linked PR, got %+v", side.linkedPRs)
	}
}

// A malformed URL is skipped, not fatal: the step's real work succeeded.
func TestPullRequestFrom_MalformedURLDoesNotFailStep(t *testing.T) {
	exec := &fakeExecutor{results: map[string]StepResult{
		"implement": {Success: true, StructuredOutput: map[string]any{"pr_url": "see the PR I opened"}},
	}}
	side := &fakeSide{}
	eng, _ := prLinkEngine(t, exec, side)

	_, success, _ := eng.RunInstance(context.Background(), prLinkWorkflow("pr_url"), model.InternalTask{ID: "c1"})
	if !success {
		t.Fatal("a malformed pr_url must not fail the step")
	}
	if len(side.linkedPRs) != 0 {
		t.Errorf("expected no linked PR, got %+v", side.linkedPRs)
	}
}

// A link that cannot be persisted is logged, not fatal — same rationale.
func TestPullRequestFrom_LinkErrorDoesNotFailStep(t *testing.T) {
	exec := &fakeExecutor{results: map[string]StepResult{
		"implement": {Success: true, StructuredOutput: map[string]any{
			"pr_url": "https://github.com/acme/widgets/pull/42",
		}},
	}}
	side := &fakeSide{linkPRErr: errors.New("db down")}
	eng, _ := prLinkEngine(t, exec, side)

	if _, success, _ := eng.RunInstance(context.Background(), prLinkWorkflow("pr_url"), model.InternalTask{ID: "c1"}); !success {
		t.Fatal("a failed PR link must not fail the step")
	}
}

// A failed step's reported PR is not linked: the URL may name a PR that was
// never opened.
func TestPullRequestFrom_FailedStepLinksNothing(t *testing.T) {
	exec := &fakeExecutor{results: map[string]StepResult{
		"implement": {Success: false, StructuredOutput: map[string]any{
			"pr_url": "https://github.com/acme/widgets/pull/42",
		}},
	}}
	side := &fakeSide{}
	eng, _ := prLinkEngine(t, exec, side)

	_, _, _ = eng.RunInstance(context.Background(), prLinkWorkflow("pr_url"), model.InternalTask{ID: "c1"})
	if len(side.linkedPRs) != 0 {
		t.Errorf("expected no linked PR from a failed step, got %+v", side.linkedPRs)
	}
}

// Without the mapping nothing is linked, even when the step emits a pr_url.
func TestPullRequestFrom_UnsetLinksNothing(t *testing.T) {
	exec := &fakeExecutor{results: map[string]StepResult{
		"implement": {Success: true, StructuredOutput: map[string]any{
			"pr_url": "https://github.com/acme/widgets/pull/42",
		}},
	}}
	side := &fakeSide{}
	eng, _ := prLinkEngine(t, exec, side)

	_, _, _ = eng.RunInstance(context.Background(), prLinkWorkflow(""), model.InternalTask{ID: "c1"})
	if len(side.linkedPRs) != 0 {
		t.Errorf("expected no linked PR without pull_request_from, got %+v", side.linkedPRs)
	}
}
