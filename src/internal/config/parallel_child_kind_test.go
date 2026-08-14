package config_test

import (
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
)

// parallelChildConfig wraps children in a workflow whose only group is the
// parallel step under test.
func parallelChildConfig(children []config.StepConfig) *config.Config {
	return &config.Config{
		Version: "1",
		Agents:  []config.AgentConfig{{ID: "eng"}},
		Workflows: []config.WorkflowConfig{{
			ID: "impl",
			Steps: []config.StepConfig{
				{ID: "implement", Agent: "eng"},
				{ID: "gate", ParallelSteps: children},
			},
		}},
	}
}

// A parallel group runs each child by its own type, but only agent and wait_for
// children are supported. The rest must be rejected at config time rather than
// failing in milliseconds at runtime and taking their siblings down with them
// under join: all (#425).
func TestParallelChild_RejectsUnsupportedKinds(t *testing.T) {
	cases := []struct {
		name  string
		child config.StepConfig
		want  string
	}{
		{
			name:  "approval",
			child: config.StepConfig{ID: "sign-off", Type: config.StepTypeApproval, Message: "ok?"},
			want:  "cannot contain a approval step",
		},
		{
			name:  "nested parallel",
			child: config.StepConfig{ID: "inner", ParallelSteps: []config.StepConfig{{ID: "a", Agent: "eng"}}},
			want:  "nested parallel: groups are not supported",
		},
		{
			name:  "for_each",
			child: config.StepConfig{ID: "loop", ForEachExpr: "${{ design.tasks }}", As: "task"},
			want:  "for_each: is not supported inside a parallel group",
		},
		{
			name:  "nested group",
			child: config.StepConfig{ID: "group", SubSteps: []config.StepConfig{{ID: "a", Agent: "eng"}}},
			want:  "nested steps: groups are not supported",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := parallelChildConfig([]config.StepConfig{
				{ID: "review", Agent: "eng"},
				tc.child,
			})
			errsContain(t, cfg.Validate(), tc.want)
		})
	}
}

// The two supported kinds still validate clean.
func TestParallelChild_AcceptsAgentAndWaitFor(t *testing.T) {
	cfg := parallelChildConfig([]config.StepConfig{
		{ID: "review", Agent: "eng"},
		{ID: "await-ci", Type: config.StepTypeWaitFor,
			WaitFor: &config.WaitForConfig{Kind: config.WaitKindCI, MaxDuration: "2h"}},
	})
	errs := cfg.Validate()
	errsNotContain(t, errs, "parallel group")
	errsNotContain(t, errs, "not supported")
}
