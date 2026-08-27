package config_test

import (
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
)

// withSplitSourceCaps models the setup #444 is about: "tracker" (Jira-like)
// owns the work items but knows nothing about pull requests, while "forge"
// (GitHub-like) hosts the PRs and can poll CI for one by number.
func withSplitSourceCaps(t *testing.T) {
	t.Helper()
	prev := config.SourceCapabilities
	config.SourceCapabilities = func(sourceType string) config.SourceCaps {
		switch sourceType {
		case "tracker":
			return config.SourceCaps{SetState: true, AddLabels: true, Approvals: true}
		case "forge":
			return config.SourceCaps{SetState: true, AddLabels: true, Approvals: true, CIWait: true, PRCIWait: true}
		}
		return config.SourceCaps{}
	}
	t.Cleanup(func() { config.SourceCapabilities = prev })
}

func ciWaitConfig(wait config.WaitForConfig) *config.Config {
	return &config.Config{
		Version: "1",
		Sources: []config.SourceConfig{
			{ID: "jira", Type: "tracker"},
			{ID: "github", Type: "forge"},
		},
		Workflows: []config.WorkflowConfig{{
			ID:      "deliver",
			Trigger: &config.TriggerConfig{Match: config.RouteMatch{Source: "jira"}},
			Steps: []config.StepConfig{
				{ID: "implement", Agent: "eng", PullRequestFrom: "pr_url"},
				{ID: "await-ci", Type: config.StepTypeWaitFor, WaitFor: &wait},
			},
		}},
	}
}

// Without ci_source the wait is still linted against the task's own source, and
// a tracker that cannot poll CI is still rejected — the #425 behaviour stands.
func TestWaitCISource_UnsetStillRejectsIncapableSource(t *testing.T) {
	withSplitSourceCaps(t)
	errsContain(t, ciWaitConfig(config.WaitForConfig{Kind: config.WaitKindCI}).Validate(), "(wait_for ci)")
}

// With ci_source the CI check is delegated to the forge, so the tracker needs no
// CI capability of its own — this config must lint clean.
func TestWaitCISource_DelegatesCapabilityToNamedSource(t *testing.T) {
	withSplitSourceCaps(t)
	errs := ciWaitConfig(config.WaitForConfig{Kind: config.WaitKindCI, CISource: "github"}).Validate()
	errsNotContain(t, errs, "(wait_for ci)")
	errsNotContain(t, errs, "ci_source")
}

func TestWaitCISource_RejectsUnknownSource(t *testing.T) {
	withSplitSourceCaps(t)
	errsContain(t, ciWaitConfig(config.WaitForConfig{CISource: "gitlab"}).Validate(),
		`ci_source "gitlab" is not a configured source`)
}

// Naming a source that cannot poll CI for a PR is caught at load time rather
// than at runtime, when the wait would have failed with the same cause.
func TestWaitCISource_RejectsSourceWithoutPRCIPolling(t *testing.T) {
	withSplitSourceCaps(t)
	errsContain(t, ciWaitConfig(config.WaitForConfig{CISource: "jira"}).Validate(),
		`cannot poll CI for a pull request`)
}

func TestWaitCISource_RejectedOnDependencyKind(t *testing.T) {
	withSplitSourceCaps(t)
	errsContain(t, ciWaitConfig(config.WaitForConfig{Kind: config.WaitKindDependency, CISource: "github"}).Validate(),
		`ci_source is only valid with kind "ci"`)
}
