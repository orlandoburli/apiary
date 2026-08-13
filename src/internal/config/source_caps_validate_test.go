package config_test

import (
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
)

// withSourceCaps points config.SourceCapabilities at a fake registry where
// "ticket" supports everything and "alerts" (read-only) supports nothing,
// restoring the previous hook on cleanup.
func withSourceCaps(t *testing.T) {
	t.Helper()
	prev := config.SourceCapabilities
	config.SourceCapabilities = func(sourceType string) config.SourceCaps {
		if sourceType == "ticket" {
			return config.SourceCaps{SetState: true, AddLabels: true, RemoveLabels: true, Approvals: true, CIWait: true, SubIssues: true}
		}
		return config.SourceCaps{}
	}
	t.Cleanup(func() { config.SourceCapabilities = prev })
}

func capsConfig(sources []config.SourceConfig, wf config.WorkflowConfig) *config.Config {
	return &config.Config{
		Version:   "1",
		Sources:   sources,
		Workflows: []config.WorkflowConfig{wf},
	}
}

func errsContain(t *testing.T, errs []error, want string) {
	t.Helper()
	for _, e := range errs {
		if strings.Contains(e.Error(), want) {
			return
		}
	}
	t.Errorf("expected an error containing %q, got %v", want, errs)
}

func errsNotContain(t *testing.T, errs []error, fragment string) {
	t.Helper()
	for _, e := range errs {
		if strings.Contains(e.Error(), fragment) {
			t.Errorf("unexpected error containing %q: %v", fragment, e)
		}
	}
}

func TestSourceCaps_PinnedReadOnlySourceRejectsWrites(t *testing.T) {
	withSourceCaps(t)

	cfg := capsConfig(
		[]config.SourceConfig{{ID: "prod-alerts", Type: "alerts"}},
		config.WorkflowConfig{
			ID:      "investigate",
			Trigger: &config.TriggerConfig{Match: config.RouteMatch{Source: "prod-alerts"}},
			Steps:   []config.StepConfig{{ID: "s1", Agent: "sre"}},
			OnComplete: &config.OnComplete{
				SetState:  "done",
				AddLabels: []string{"triaged"},
			},
		},
	)
	errs := cfg.Validate()
	errsContain(t, errs, "on_complete.set_state")
	errsContain(t, errs, "label writes")
}

func TestSourceCaps_PinnedReadOnlySourceRejectsApprovalAndCIWait(t *testing.T) {
	withSourceCaps(t)

	cfg := capsConfig(
		[]config.SourceConfig{{ID: "prod-alerts", Type: "alerts"}},
		config.WorkflowConfig{
			ID:      "investigate",
			Trigger: &config.TriggerConfig{Match: config.RouteMatch{Source: "prod-alerts"}},
			Steps: []config.StepConfig{
				{ID: "gate", Type: config.StepTypeApproval, Message: "ok?", ResumeOn: &config.ApprovalTrigger{CommentContains: "approved"}},
				{ID: "ci", Type: config.StepTypeWaitFor, WaitFor: &config.WaitForConfig{Kind: config.WaitKindCI}},
			},
		},
	)
	errs := cfg.Validate()
	errsContain(t, errs, "(approval)")
	errsContain(t, errs, "(wait_for ci)")
}

func TestSourceCaps_PinnedTicketSourceAllowsWrites(t *testing.T) {
	withSourceCaps(t)

	cfg := capsConfig(
		[]config.SourceConfig{{ID: "gh", Type: "ticket"}, {ID: "prod-alerts", Type: "alerts"}},
		config.WorkflowConfig{
			ID:         "fix",
			Trigger:    &config.TriggerConfig{Match: config.RouteMatch{Source: "gh"}},
			Steps:      []config.StepConfig{{ID: "s1", Agent: "eng"}},
			OnComplete: &config.OnComplete{SetState: "done"},
		},
	)
	errsNotContain(t, cfg.Validate(), "capability")
}

func TestSourceCaps_UnpinnedWorkflowAllowedWhenSomeSourceSupports(t *testing.T) {
	withSourceCaps(t)

	// Mixed sources, no pin: the item's origin is runtime information, so the
	// lint stays quiet as long as one source supports the feature.
	cfg := capsConfig(
		[]config.SourceConfig{{ID: "gh", Type: "ticket"}, {ID: "prod-alerts", Type: "alerts"}},
		config.WorkflowConfig{
			ID:         "fix",
			Steps:      []config.StepConfig{{ID: "s1", Agent: "eng"}},
			OnComplete: &config.OnComplete{SetState: "done"},
		},
	)
	errsNotContain(t, cfg.Validate(), "capability")
}

func TestSourceCaps_UnpinnedWorkflowRejectedWhenNoSourceSupports(t *testing.T) {
	withSourceCaps(t)

	cfg := capsConfig(
		[]config.SourceConfig{{ID: "prod-alerts", Type: "alerts"}},
		config.WorkflowConfig{
			ID:         "fix",
			Steps:      []config.StepConfig{{ID: "s1", Agent: "eng"}},
			OnComplete: &config.OnComplete{SetState: "done"},
		},
	)
	errsContain(t, cfg.Validate(), "no configured source supports")
}

func TestSourceCaps_SkippedWhenHookNil(t *testing.T) {
	prev := config.SourceCapabilities
	config.SourceCapabilities = nil
	t.Cleanup(func() { config.SourceCapabilities = prev })

	cfg := capsConfig(
		[]config.SourceConfig{{ID: "prod-alerts", Type: "alerts"}},
		config.WorkflowConfig{
			ID:         "fix",
			Steps:      []config.StepConfig{{ID: "s1", Agent: "eng"}},
			OnComplete: &config.OnComplete{SetState: "done"},
		},
	)
	errsNotContain(t, cfg.Validate(), "capability")
	errsNotContain(t, cfg.Validate(), "no configured source supports")
}

// A wait_for ci step nested inside a parallel: group needs the CI capability
// just like a top-level one. Before #425 the lint walked only the top-level
// step list, so a Jira-sourced workflow with a `wait_for {kind: ci}` inside a
// parallel group validated clean and failed at runtime instead.
func TestSourceCaps_CIWaitNestedInParallelIsLinted(t *testing.T) {
	withSourceCaps(t)

	cfg := capsConfig(
		[]config.SourceConfig{{ID: "tickets", Type: "alerts"}},
		config.WorkflowConfig{
			ID: "impl",
			Steps: []config.StepConfig{
				{ID: "implement", Agent: "eng"},
				{ID: "gate", ParallelSteps: []config.StepConfig{
					{ID: "review", Agent: "eng"},
					{ID: "await-ci", Type: config.StepTypeWaitFor,
						WaitFor: &config.WaitForConfig{Kind: config.WaitKindCI}},
				}},
			},
		},
	)
	errs := cfg.Validate()
	errsContain(t, errs, `step "await-ci" (wait_for ci)`)
	errsContain(t, errs, "no configured source supports")
}

// The same lint reaches a for_each body.
func TestSourceCaps_CIWaitNestedInForeachIsLinted(t *testing.T) {
	withSourceCaps(t)

	cfg := capsConfig(
		[]config.SourceConfig{{ID: "tickets", Type: "alerts"}},
		config.WorkflowConfig{
			ID: "impl",
			Steps: []config.StepConfig{
				{ID: "design", Agent: "eng"},
				{ID: "each", ForEachExpr: "${{ design.tasks }}", As: "task", SubSteps: []config.StepConfig{
					{ID: "await-ci", Type: config.StepTypeWaitFor,
						WaitFor: &config.WaitForConfig{Kind: config.WaitKindCI}},
				}},
			},
		},
	)
	errsContain(t, cfg.Validate(), `step "await-ci" (wait_for ci)`)
}

// A supported source keeps a nested wait_for ci valid.
func TestSourceCaps_CIWaitNestedInParallelAcceptedWhenSupported(t *testing.T) {
	withSourceCaps(t)

	cfg := capsConfig(
		[]config.SourceConfig{{ID: "tickets", Type: "ticket"}},
		config.WorkflowConfig{
			ID: "impl",
			Steps: []config.StepConfig{
				{ID: "implement", Agent: "eng"},
				{ID: "gate", ParallelSteps: []config.StepConfig{
					{ID: "review", Agent: "eng"},
					{ID: "await-ci", Type: config.StepTypeWaitFor,
						WaitFor: &config.WaitForConfig{Kind: config.WaitKindCI}},
				}},
			},
		},
	)
	errsNotContain(t, cfg.Validate(), "wait_for ci")
}
