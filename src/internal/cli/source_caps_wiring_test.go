package cli

import (
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"

	// Register the real source adapters, as cmd/apiary does, so the capability
	// probe in init() inspects actual adapter instances.
	_ "github.com/orlandoburli/apiary/internal/source/dynatrace"
	_ "github.com/orlandoburli/apiary/internal/source/github"
	_ "github.com/orlandoburli/apiary/internal/source/prometheus"
)

// writeCaps strips the read-only capabilities, leaving only the write ones the
// read-only-source lint is about. Resolvable (source.ItemResolver, backing
// interrupt_on_resolve) is a read capability: a monitoring source having it
// does not make it writable.
func writeCaps(c config.SourceCaps) config.SourceCaps {
	c.Resolvable = false
	return c
}

// TestSourceCapsWiring_RealAdapters verifies the #357 capability probe against
// the real registry: the prometheus alert adapter must present as fully
// read-only, while github supports every write capability. If a write method
// is ever added to the prometheus Adapter (or removed from github), this
// catches the lint silently changing behavior.
func TestSourceCapsWiring_RealAdapters(t *testing.T) {
	if config.SourceCapabilities == nil {
		t.Fatal("config.SourceCapabilities not wired by cli init()")
	}

	promCaps := config.SourceCapabilities("prometheus")
	if writeCaps(promCaps) != (config.SourceCaps{}) {
		t.Errorf("prometheus write caps = %+v, want all-false (read-only alert source)", writeCaps(promCaps))
	}
	// Alerts resolve, and the adapter can say so — this is what
	// interrupt_on_resolve is validated against.
	if !promCaps.Resolvable {
		t.Error("prometheus caps.Resolvable = false, want true (adapter implements source.ItemResolver)")
	}

	if caps := config.SourceCapabilities("dynatrace"); caps != (config.SourceCaps{}) {
		t.Errorf("dynatrace caps = %+v, want all-false (read-only problem source)", caps)
	}

	want := config.SourceCaps{SetState: true, AddLabels: true, RemoveLabels: true, Approvals: true, CIWait: true, SubIssues: true}
	if caps := config.SourceCapabilities("github"); caps != want {
		t.Errorf("github caps = %+v, want %+v", caps, want)
	}

	if caps := config.SourceCapabilities("no-such-adapter"); caps != (config.SourceCaps{}) {
		t.Errorf("unknown adapter caps = %+v, want all-false", caps)
	}
}

// TestSourceCapsWiring_ValidateRejectsAlertWrites is the end-to-end acceptance
// check for issue #357: `apiary validate` (cli wiring + real adapters) must
// reject set_state, approval steps, and wait_for ci against a workflow pinned
// to a prometheus source.
func TestSourceCapsWiring_ValidateRejectsAlertWrites(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Sources: []config.SourceConfig{{ID: "prod-alerts", Type: "prometheus"}},
		Agents:  []config.AgentConfig{{ID: "sre", Model: "m"}},
		Workflows: []config.WorkflowConfig{{
			ID:      "investigate",
			Trigger: &config.TriggerConfig{Match: config.RouteMatch{Source: "prod-alerts"}},
			Steps: []config.StepConfig{
				{ID: "gate", Type: config.StepTypeApproval, Message: "ok?", ResumeOn: &config.ApprovalTrigger{CommentContains: "approved"}},
				{ID: "ci", Type: config.StepTypeWaitFor, WaitFor: &config.WaitForConfig{Kind: config.WaitKindCI}},
			},
			OnComplete: &config.OnComplete{SetState: "done"},
		}},
	}

	errs := cfg.Validate()
	for _, want := range []string{"on_complete.set_state", "(approval)", "(wait_for ci)"} {
		found := false
		for _, e := range errs {
			if strings.Contains(e.Error(), want) && strings.Contains(e.Error(), "prod-alerts") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected a capability error for %s against prod-alerts, got: %v", want, errs)
		}
	}
}

// TestSourceCapsWiring_GithubWorkflowPasses is the mirror case: the same
// features pinned to a github source produce no capability errors.
func TestSourceCapsWiring_GithubWorkflowPasses(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Sources: []config.SourceConfig{{ID: "gh", Type: "github"}},
		Agents:  []config.AgentConfig{{ID: "eng", Model: "m"}},
		Workflows: []config.WorkflowConfig{{
			ID:      "fix",
			Trigger: &config.TriggerConfig{Match: config.RouteMatch{Source: "gh"}},
			Steps: []config.StepConfig{
				{ID: "work", Agent: "eng"},
				{ID: "ci", Type: config.StepTypeWaitFor, WaitFor: &config.WaitForConfig{Kind: config.WaitKindCI}},
			},
			OnComplete: &config.OnComplete{SetState: "done"},
		}},
	}

	for _, e := range cfg.Validate() {
		if strings.Contains(e.Error(), "capability") {
			t.Errorf("unexpected capability error for github-pinned workflow: %v", e)
		}
	}
}
