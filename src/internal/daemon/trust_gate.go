package daemon

import (
	"fmt"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
)

// isTrustedAssociation reports whether a GitHub author_association value
// belongs to the trusted collaborator set: OWNER, MEMBER, or COLLABORATOR.
// Any other value (CONTRIBUTOR, FIRST_TIME_CONTRIBUTOR, FIRST_TIMER, NONE, or
// an empty string) is considered untrusted.
func isTrustedAssociation(assoc string) bool {
	switch assoc {
	case "OWNER", "MEMBER", "COLLABORATOR":
		return true
	default:
		return false
	}
}

// trustGateStepID is the synthetic step identifier injected by withTrustGate.
// It must not collide with any user-defined step ID; the leading underscore
// makes a collision by YAML authoring effectively impossible.
const trustGateStepID = "_trust_gate"

const trustGateDefaultMsg = "A workflow run triggered by a non-collaborator requires human approval before the agent executes.\n\nReply with `/approve` to allow or `/reject` to discard."

// withTrustGate returns wf unchanged when:
//   - the trust gate is disabled (cfg.Enabled == false), or
//   - cell carries no author_association metadata (non-GitHub sources pass through), or
//   - the author's association is in the trusted set (OWNER/MEMBER/COLLABORATOR).
//
// For untrusted GitHub issue authors it prepends a synthetic approval step to
// wf.Steps and adds it as an explicit dependency of every root step (steps with
// neither DependsOn nor SeqDependsOn). This ensures the DAG engine parks the
// run at the approval step before any shell-capable agent step can execute, and
// the run is resolvable through the full multi-channel approval infrastructure
// (dashboard, webhook, or comment trigger).
func withTrustGate(wf config.WorkflowConfig, cell model.SourceItem, cfg config.TrustGateSettings) config.WorkflowConfig {
	if !cfg.Enabled {
		return wf
	}
	assoc, ok := cell.Metadata["author_association"].(string)
	if !ok || assoc == "" {
		return wf
	}
	if isTrustedAssociation(assoc) {
		return wf
	}

	login, _ := cell.Metadata["author_login"].(string)
	msg := cfg.Message
	if msg == "" {
		if login != "" {
			msg = fmt.Sprintf("@%s is not a repository collaborator (association: %s). %s", login, assoc, trustGateDefaultMsg)
		} else {
			msg = trustGateDefaultMsg
		}
	}

	gate := config.StepConfig{
		ID:      trustGateStepID,
		Type:    config.StepTypeApproval,
		Name:    "Trust gate: non-collaborator approval required",
		Message: msg,
		ResumeOn: &config.ApprovalTrigger{
			CommentContains: "/approve",
		},
		AbortOn: &config.ApprovalTrigger{
			CommentContains: "/reject",
		},
		Approvers: cfg.Approvers,
	}

	// Attach the gate as a dependency of every root step (steps that currently
	// have no explicit or sequential predecessor). This covers both v1 workflows
	// (explicit DependsOn) and v2 lowered workflows (SeqDependsOn chains).
	steps := make([]config.StepConfig, 0, len(wf.Steps)+1)
	steps = append(steps, gate)
	for _, s := range wf.Steps {
		if len(s.DependsOn) == 0 && len(s.SeqDependsOn) == 0 {
			s.DependsOn = []string{trustGateStepID}
		}
		steps = append(steps, s)
	}
	wf.Steps = steps
	return wf
}
