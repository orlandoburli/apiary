package config_test

import (
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
)

func prFromConfig(step config.StepConfig) *config.Config {
	return &config.Config{
		Version: "1",
		Agents:  []config.AgentConfig{{ID: "eng"}},
		Workflows: []config.WorkflowConfig{{
			ID:    "impl",
			Steps: []config.StepConfig{step},
		}},
	}
}

// pull_request_from must name a field the step declares, or the PR link would
// silently never appear.
func TestPullRequestFrom_RejectsUndeclaredField(t *testing.T) {
	cfg := prFromConfig(config.StepConfig{
		ID: "implement", Agent: "eng", PullRequestFrom: "pr_link",
		OutputSchema: &config.OutputSchema{
			Type:       "object",
			Properties: map[string]config.SchemaField{"pr_url": {Type: "string"}},
		},
	})
	errsContain(t, cfg.Validate(), `pull_request_from references "pr_link"`)
}

func TestPullRequestFrom_AcceptsDeclaredField(t *testing.T) {
	cfg := prFromConfig(config.StepConfig{
		ID: "implement", Agent: "eng", PullRequestFrom: "pr_url",
		OutputSchema: &config.OutputSchema{
			Type:       "object",
			Properties: map[string]config.SchemaField{"pr_url": {Type: "string"}},
		},
	})
	errsNotContain(t, cfg.Validate(), "pull_request_from")
}

// A step with no declared schema is accepted: the field is read from whatever
// the agent emits.
func TestPullRequestFrom_AcceptedWithoutOutputSchema(t *testing.T) {
	cfg := prFromConfig(config.StepConfig{ID: "implement", Agent: "eng", PullRequestFrom: "pr_url"})
	errsNotContain(t, cfg.Validate(), "pull_request_from")
}

// Only an agent step produces the structured output the mapping reads.
func TestPullRequestFrom_RejectedOnNonAgentStep(t *testing.T) {
	cfg := prFromConfig(config.StepConfig{
		ID: "await-ci", Type: config.StepTypeWaitFor, PullRequestFrom: "pr_url",
		WaitFor: &config.WaitForConfig{Kind: config.WaitKindCI},
	})
	errsContain(t, cfg.Validate(), "pull_request_from is only valid on an agent step")
}
