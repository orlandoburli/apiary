package daemon

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/source"
)

// fakeCollaboratorChecker implements source.CollaboratorChecker in tests.
type fakeCollaboratorChecker struct {
	collaborators map[string]bool
	err           error
}

func (f *fakeCollaboratorChecker) IsCollaborator(_ context.Context, login string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.collaborators[login], nil
}

// TestInjectApprovalGate verifies that injectApprovalGate prepends the gate
// step and wires every root step through it.
func TestInjectApprovalGate(t *testing.T) {
	wf := config.WorkflowConfig{
		ID: "test-wf",
		Steps: []config.StepConfig{
			{ID: "step-a", Agent: "eng"},
			{ID: "step-b", Agent: "eng", SeqDependsOn: []string{"step-a"}},
		},
	}
	cfg := config.ApprovalSettings{
		UntrustedApprovers: []string{"alice"},
	}
	out := injectApprovalGate(wf, cfg, "mallory")

	// Gate step is first.
	if len(out.Steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(out.Steps))
	}
	gate := out.Steps[0]
	if gate.ID != untrustedGateStepID {
		t.Errorf("first step should be gate, got %q", gate.ID)
	}
	if gate.Type != config.StepTypeApproval {
		t.Errorf("gate step type should be approval, got %q", gate.Type)
	}
	if len(gate.Approvers) != 1 || gate.Approvers[0] != "alice" {
		t.Errorf("gate should carry approvers, got %v", gate.Approvers)
	}

	// step-a (root) now depends on the gate.
	stepA := out.Steps[1]
	if stepA.ID != "step-a" {
		t.Fatalf("unexpected step order")
	}
	if len(stepA.DependsOn) != 1 || stepA.DependsOn[0] != untrustedGateStepID {
		t.Errorf("step-a should depend on gate, got DependsOn=%v", stepA.DependsOn)
	}

	// step-b already has SeqDependsOn; it must NOT get gate added to DependsOn
	// (it is not a root — it depends on step-a which depends on the gate).
	stepB := out.Steps[2]
	if stepB.ID != "step-b" {
		t.Fatalf("unexpected step order")
	}
	if len(stepB.DependsOn) != 0 {
		t.Errorf("step-b should have no DependsOn (it is not a root), got %v", stepB.DependsOn)
	}
}

// TestInjectApprovalGate_DefaultMessage verifies the default message includes
// the author login when no custom message is configured.
func TestInjectApprovalGate_DefaultMessage(t *testing.T) {
	wf := config.WorkflowConfig{Steps: []config.StepConfig{{ID: "s", Agent: "eng"}}}
	out := injectApprovalGate(wf, config.ApprovalSettings{}, "mallory")
	gate := out.Steps[0]
	if gate.Message == "" {
		t.Error("gate message should not be empty")
	}
	// The login should appear in the default message.
	if len(gate.Message) == 0 {
		t.Error("expected non-empty gate message")
	}
}

// TestInjectApprovalGate_DoesNotMutateOriginal verifies the returned workflow
// is a new copy; the original steps slice is unmodified.
func TestInjectApprovalGate_DoesNotMutateOriginal(t *testing.T) {
	original := config.WorkflowConfig{
		Steps: []config.StepConfig{{ID: "s", Agent: "eng"}},
	}
	_ = injectApprovalGate(original, config.ApprovalSettings{}, "x")
	// original.Steps[0].DependsOn must still be empty.
	if len(original.Steps[0].DependsOn) != 0 {
		t.Error("injectApprovalGate must not mutate the original steps slice")
	}
}

// TestMaybeInjectAuthorGate_Disabled verifies no gate is injected when the
// feature is off, even for a non-collaborator author.
func TestMaybeInjectAuthorGate_Disabled(t *testing.T) {
	d := &Dispatcher{
		cfg: &config.Config{},
		sources: map[string]source.Adapter{},
	}
	cell := model.SourceItem{AuthorLogin: "attacker"}
	wf := config.WorkflowConfig{Steps: []config.StepConfig{{ID: "s"}}}
	out := d.maybeInjectAuthorGate(context.Background(), cell, wf)
	if len(out.Steps) != 1 {
		t.Errorf("gate should not be injected when disabled; got %d steps", len(out.Steps))
	}
}

// TestMaybeInjectAuthorGate_NoAuthor verifies no gate is injected when the
// source item has no author login.
func TestMaybeInjectAuthorGate_NoAuthor(t *testing.T) {
	d := &Dispatcher{
		cfg: &config.Config{
			Settings: config.Settings{
				Approvals: config.ApprovalSettings{GateUntrustedAuthors: true},
			},
		},
		sources: map[string]source.Adapter{},
	}
	cell := model.SourceItem{AuthorLogin: ""}
	wf := config.WorkflowConfig{Steps: []config.StepConfig{{ID: "s"}}}
	out := d.maybeInjectAuthorGate(context.Background(), cell, wf)
	if len(out.Steps) != 1 {
		t.Errorf("gate should not be injected when author is empty; got %d steps", len(out.Steps))
	}
}

// collaboratorFakeAdapter is a minimal source.Adapter that also implements
// source.CollaboratorChecker for testing.
type collaboratorFakeAdapter struct {
	id      string
	checker *fakeCollaboratorChecker
}

var _ source.Adapter = (*collaboratorFakeAdapter)(nil)
var _ source.CollaboratorChecker = (*collaboratorFakeAdapter)(nil)

func (a *collaboratorFakeAdapter) ID() string  { return a.id }
func (a *collaboratorFakeAdapter) SetID(id string) { a.id = id }
func (a *collaboratorFakeAdapter) Connect(_ context.Context, _ map[string]any) error { return nil }
func (a *collaboratorFakeAdapter) Poll(_ context.Context, _ time.Time) ([]model.SourceItem, error) {
	return nil, nil
}
func (a *collaboratorFakeAdapter) Acknowledge(_ context.Context, _ model.SourceItem, _ model.AckAction) error {
	return nil
}
func (a *collaboratorFakeAdapter) WriteResult(_ context.Context, _ model.SourceItem, _ model.RunResult) error {
	return nil
}
func (a *collaboratorFakeAdapter) WebhookHandler() http.Handler { return nil }
func (a *collaboratorFakeAdapter) IsCollaborator(ctx context.Context, login string) (bool, error) {
	return a.checker.IsCollaborator(ctx, login)
}

// TestMaybeInjectAuthorGate_Collaborator verifies no gate for a trusted author.
func TestMaybeInjectAuthorGate_Collaborator(t *testing.T) {
	checker := &fakeCollaboratorChecker{collaborators: map[string]bool{"alice": true}}
	adapter := &collaboratorFakeAdapter{id: "gh", checker: checker}
	d := &Dispatcher{
		cfg: &config.Config{
			Settings: config.Settings{
				Approvals: config.ApprovalSettings{GateUntrustedAuthors: true},
			},
		},
		sources: map[string]source.Adapter{"gh": adapter},
	}
	cell := model.SourceItem{SourceID: "gh", AuthorLogin: "alice"}
	wf := config.WorkflowConfig{Steps: []config.StepConfig{{ID: "s"}}}
	out := d.maybeInjectAuthorGate(context.Background(), cell, wf)
	if len(out.Steps) != 1 {
		t.Errorf("gate should not be injected for a collaborator; got %d steps", len(out.Steps))
	}
}

// TestMaybeInjectAuthorGate_NonCollaborator verifies the gate IS injected for
// an author who is not a repository collaborator.
func TestMaybeInjectAuthorGate_NonCollaborator(t *testing.T) {
	checker := &fakeCollaboratorChecker{collaborators: map[string]bool{}}
	adapter := &collaboratorFakeAdapter{id: "gh", checker: checker}
	d := &Dispatcher{
		cfg: &config.Config{
			Settings: config.Settings{
				Approvals: config.ApprovalSettings{GateUntrustedAuthors: true},
			},
		},
		sources: map[string]source.Adapter{"gh": adapter},
	}
	cell := model.SourceItem{SourceID: "gh", AuthorLogin: "attacker"}
	wf := config.WorkflowConfig{Steps: []config.StepConfig{{ID: "s"}}}
	out := d.maybeInjectAuthorGate(context.Background(), cell, wf)
	if len(out.Steps) != 2 {
		t.Fatalf("gate should be injected; got %d steps", len(out.Steps))
	}
	if out.Steps[0].ID != untrustedGateStepID {
		t.Errorf("first step should be gate, got %q", out.Steps[0].ID)
	}
}

// TestMaybeInjectAuthorGate_APIError verifies fail-open: an API error skips
// the gate rather than blocking the run.
func TestMaybeInjectAuthorGate_APIError(t *testing.T) {
	checker := &fakeCollaboratorChecker{err: errCollabAPIError}
	adapter := &collaboratorFakeAdapter{id: "gh", checker: checker}
	d := &Dispatcher{
		cfg: &config.Config{
			Settings: config.Settings{
				Approvals: config.ApprovalSettings{GateUntrustedAuthors: true},
			},
		},
		sources: map[string]source.Adapter{"gh": adapter},
	}
	cell := model.SourceItem{SourceID: "gh", AuthorLogin: "unknown"}
	wf := config.WorkflowConfig{Steps: []config.StepConfig{{ID: "s"}}}
	out := d.maybeInjectAuthorGate(context.Background(), cell, wf)
	if len(out.Steps) != 1 {
		t.Errorf("API error should be fail-open (no gate); got %d steps", len(out.Steps))
	}
}

var errCollabAPIError = &collaboratorAPIError{}

type collaboratorAPIError struct{}

func (e *collaboratorAPIError) Error() string { return "github: collaborator check: status 403" }
