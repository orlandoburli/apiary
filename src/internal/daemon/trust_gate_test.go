package daemon

import (
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
)

// TestIsTrustedAssociation verifies the GitHub collaborator trust set.
func TestIsTrustedAssociation(t *testing.T) {
	trusted := []string{"OWNER", "MEMBER", "COLLABORATOR"}
	for _, assoc := range trusted {
		if !isTrustedAssociation(assoc) {
			t.Errorf("isTrustedAssociation(%q) = false, want true", assoc)
		}
	}

	untrusted := []string{"CONTRIBUTOR", "FIRST_TIME_CONTRIBUTOR", "FIRST_TIMER", "NONE", ""}
	for _, assoc := range untrusted {
		if isTrustedAssociation(assoc) {
			t.Errorf("isTrustedAssociation(%q) = true, want false", assoc)
		}
	}
}

// wf2steps is a helper that builds a minimal two-step workflow.
func wf2steps() config.WorkflowConfig {
	return config.WorkflowConfig{
		ID: "wf-test",
		Steps: []config.StepConfig{
			{ID: "classify", Agent: "bot"},
			{ID: "implement", Agent: "bot", SeqDependsOn: []string{"classify"}},
		},
	}
}

// untrustedCell returns a SourceItem whose author_association is NONE.
func untrustedCell(login string) model.SourceItem {
	return model.SourceItem{
		ID:       "42",
		SourceID: "gh",
		Title:    "Untrusted issue",
		Metadata: map[string]any{
			"author_association": "NONE",
			"author_login":       login,
		},
	}
}

// trustedCell returns a SourceItem whose author_association is MEMBER.
func trustedCell() model.SourceItem {
	return model.SourceItem{
		ID:       "43",
		SourceID: "gh",
		Title:    "Trusted issue",
		Metadata: map[string]any{
			"author_association": "MEMBER",
			"author_login":       "team-member",
		},
	}
}

// noAssocCell returns a SourceItem with no Metadata (non-GitHub source).
func noAssocCell() model.SourceItem {
	return model.SourceItem{ID: "44", SourceID: "jira", Title: "Jira ticket"}
}

func enabledGate() config.TrustGateSettings {
	return config.TrustGateSettings{Enabled: true}
}

// TestWithTrustGate_Disabled verifies withTrustGate is a no-op when disabled.
func TestWithTrustGate_Disabled(t *testing.T) {
	wf := wf2steps()
	got := withTrustGate(wf, untrustedCell("outsider"), config.TrustGateSettings{Enabled: false})
	if len(got.Steps) != 2 {
		t.Errorf("disabled gate: got %d steps, want 2", len(got.Steps))
	}
}

// TestWithTrustGate_TrustedPassThrough verifies that trusted authors are not gated.
func TestWithTrustGate_TrustedPassThrough(t *testing.T) {
	wf := wf2steps()
	got := withTrustGate(wf, trustedCell(), enabledGate())
	if len(got.Steps) != 2 {
		t.Errorf("trusted author: got %d steps, want 2", len(got.Steps))
	}
	if got.Steps[0].ID == trustGateStepID {
		t.Errorf("trusted author: unexpected trust gate step prepended")
	}
}

// TestWithTrustGate_NoAssocPassThrough verifies that items without author_association pass through.
func TestWithTrustGate_NoAssocPassThrough(t *testing.T) {
	wf := wf2steps()
	got := withTrustGate(wf, noAssocCell(), enabledGate())
	if len(got.Steps) != 2 {
		t.Errorf("no-assoc cell: got %d steps, want 2", len(got.Steps))
	}
}

// TestWithTrustGate_PrependsApprovalStep verifies that an untrusted author
// gets a trust gate approval step prepended before the workflow's own steps.
func TestWithTrustGate_PrependsApprovalStep(t *testing.T) {
	wf := wf2steps()
	got := withTrustGate(wf, untrustedCell("outsider"), enabledGate())

	if len(got.Steps) != 3 {
		t.Fatalf("untrusted author: got %d steps, want 3", len(got.Steps))
	}
	gate := got.Steps[0]
	if gate.ID != trustGateStepID {
		t.Errorf("step[0].ID = %q, want %q", gate.ID, trustGateStepID)
	}
	if gate.Type != config.StepTypeApproval {
		t.Errorf("step[0].Type = %q, want %q", gate.Type, config.StepTypeApproval)
	}
	if gate.ResumeOn == nil || gate.ResumeOn.CommentContains != "/approve" {
		t.Errorf("gate.ResumeOn = %+v, want CommentContains=/approve", gate.ResumeOn)
	}
	if gate.AbortOn == nil || gate.AbortOn.CommentContains != "/reject" {
		t.Errorf("gate.AbortOn = %+v, want CommentContains=/reject", gate.AbortOn)
	}
}

// TestWithTrustGate_RootStepDependsOnGate verifies that the first step of the
// original workflow is wired to depend on the trust gate step so the DAG engine
// cannot dispatch it before the approval resolves.
func TestWithTrustGate_RootStepDependsOnGate(t *testing.T) {
	wf := wf2steps()
	got := withTrustGate(wf, untrustedCell("outsider"), enabledGate())

	// The original first step (classify) had no deps; it should now depend on gate.
	classify := got.Steps[1]
	if classify.ID != "classify" {
		t.Fatalf("got.Steps[1].ID = %q, want %q", classify.ID, "classify")
	}
	foundGateDep := false
	for _, dep := range classify.DependsOn {
		if dep == trustGateStepID {
			foundGateDep = true
		}
	}
	if !foundGateDep {
		t.Errorf("classify.DependsOn = %v, want %q in there", classify.DependsOn, trustGateStepID)
	}
}

// TestWithTrustGate_NonRootStepUnchanged verifies that a step that already has
// deps is not modified — only root steps get the gate wired in.
func TestWithTrustGate_NonRootStepUnchanged(t *testing.T) {
	wf := wf2steps()
	got := withTrustGate(wf, untrustedCell("outsider"), enabledGate())

	// implement depends on classify, so it is not a root step.
	implement := got.Steps[2]
	if implement.ID != "implement" {
		t.Fatalf("got.Steps[2].ID = %q, want %q", implement.ID, "implement")
	}
	for _, dep := range implement.DependsOn {
		if dep == trustGateStepID {
			t.Errorf("implement.DependsOn = %v: non-root step should not have gate dep", implement.DependsOn)
		}
	}
}

// TestWithTrustGate_MessageContainsLogin verifies the default approval message
// mentions the author login when one is provided.
func TestWithTrustGate_MessageContainsLogin(t *testing.T) {
	wf := wf2steps()
	got := withTrustGate(wf, untrustedCell("ext-user"), enabledGate())
	gate := got.Steps[0]
	if gate.Message == "" {
		t.Fatal("gate.Message is empty")
	}
	if !strings.Contains(gate.Message, "ext-user") {
		t.Errorf("gate.Message = %q, want it to contain %q", gate.Message, "ext-user")
	}
}

// TestWithTrustGate_CustomMessage verifies a configured message is used verbatim.
func TestWithTrustGate_CustomMessage(t *testing.T) {
	wf := wf2steps()
	cfg := config.TrustGateSettings{Enabled: true, Message: "custom approval needed"}
	got := withTrustGate(wf, untrustedCell("outsider"), cfg)
	gate := got.Steps[0]
	if gate.Message != "custom approval needed" {
		t.Errorf("gate.Message = %q, want %q", gate.Message, "custom approval needed")
	}
}

// TestWithTrustGate_ApproversForwarded verifies that configured approvers are
// forwarded to the synthetic approval step.
func TestWithTrustGate_ApproversForwarded(t *testing.T) {
	wf := wf2steps()
	cfg := config.TrustGateSettings{Enabled: true, Approvers: []string{"alice", "bob"}}
	got := withTrustGate(wf, untrustedCell("outsider"), cfg)
	gate := got.Steps[0]
	if len(gate.Approvers) != 2 || gate.Approvers[0] != "alice" || gate.Approvers[1] != "bob" {
		t.Errorf("gate.Approvers = %v, want [alice bob]", gate.Approvers)
	}
}

// TestWithTrustGate_OriginalWorkflowUnmutated verifies that the original
// WorkflowConfig is not mutated in place.
func TestWithTrustGate_OriginalWorkflowUnmutated(t *testing.T) {
	wf := wf2steps()
	_ = withTrustGate(wf, untrustedCell("outsider"), enabledGate())
	if len(wf.Steps) != 2 {
		t.Errorf("original wf.Steps length changed to %d; withTrustGate must not mutate the caller's slice", len(wf.Steps))
	}
}
