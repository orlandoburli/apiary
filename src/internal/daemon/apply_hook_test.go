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

// fakeHookSource implements Adapter plus the optional LabelAdder, LabelRemover,
// and StateSetter interfaces ApplyHook fans out to, recording the order of the
// label operations.
type fakeHookSource struct {
	ops      []string
	added    []string
	removed  []string
	stateSet string
}

func (f *fakeHookSource) ID() string                                    { return "fake" }
func (f *fakeHookSource) Connect(context.Context, map[string]any) error { return nil }
func (f *fakeHookSource) Poll(context.Context, time.Time) ([]model.SourceItem, error) {
	return nil, nil
}
func (f *fakeHookSource) Acknowledge(context.Context, model.SourceItem, model.AckAction) error {
	return nil
}
func (f *fakeHookSource) WriteResult(context.Context, model.SourceItem, model.RunResult) error {
	return nil
}
func (f *fakeHookSource) WebhookHandler() http.Handler { return nil }
func (f *fakeHookSource) AddLabels(_ context.Context, _ model.SourceItem, labels []string) error {
	f.ops = append(f.ops, "add")
	f.added = append(f.added, labels...)
	return nil
}
func (f *fakeHookSource) RemoveLabels(_ context.Context, _ model.SourceItem, labels []string) error {
	f.ops = append(f.ops, "remove")
	f.removed = append(f.removed, labels...)
	return nil
}
func (f *fakeHookSource) SetState(_ context.Context, _ model.SourceItem, state string) error {
	f.stateSet = state
	return nil
}

// fakeBareSource implements only the base Adapter interface, so every optional
// hook capability must be skipped without error.
type fakeBareSource struct{}

func (f *fakeBareSource) ID() string                                    { return "bare" }
func (f *fakeBareSource) Connect(context.Context, map[string]any) error { return nil }
func (f *fakeBareSource) Poll(context.Context, time.Time) ([]model.SourceItem, error) {
	return nil, nil
}
func (f *fakeBareSource) Acknowledge(context.Context, model.SourceItem, model.AckAction) error {
	return nil
}
func (f *fakeBareSource) WriteResult(context.Context, model.SourceItem, model.RunResult) error {
	return nil
}
func (f *fakeBareSource) WebhookHandler() http.Handler { return nil }

func TestApplyHook_RemovesLabelsAfterAdditions(t *testing.T) {
	fake := &fakeHookSource{}
	d := &Dispatcher{sources: map[string]source.Adapter{"fake": fake}}

	task := model.InternalTask{ID: "T-1", Title: "Spec"}
	bindings := []model.SourceBinding{{SourceID: "fake", SourceItemID: "ISSUE-1"}}
	hook := config.OnComplete{
		SetState:     "done",
		AddLabels:    []string{"ai-spec-done"},
		RemoveLabels: []string{"create-spec"},
	}

	if err := (&wfSideEffects{d: d}).ApplyHook(context.Background(), task, bindings, hook); err != nil {
		t.Fatalf("ApplyHook: %v", err)
	}

	if len(fake.added) != 1 || fake.added[0] != "ai-spec-done" {
		t.Errorf("added labels = %v, want [ai-spec-done]", fake.added)
	}
	if len(fake.removed) != 1 || fake.removed[0] != "create-spec" {
		t.Errorf("removed labels = %v, want [create-spec]", fake.removed)
	}
	if fake.stateSet != "done" {
		t.Errorf("state set = %q, want done", fake.stateSet)
	}
	// A label listed in both add_labels and remove_labels must end up removed.
	if len(fake.ops) != 2 || fake.ops[0] != "add" || fake.ops[1] != "remove" {
		t.Errorf("op order = %v, want [add remove]", fake.ops)
	}
}

func TestApplyHook_SkipsSourcesWithoutLabelRemover(t *testing.T) {
	d := &Dispatcher{sources: map[string]source.Adapter{"bare": &fakeBareSource{}}}

	task := model.InternalTask{ID: "T-2", Title: "Spec"}
	bindings := []model.SourceBinding{{SourceID: "bare", SourceItemID: "ISSUE-2"}}
	hook := config.OnComplete{RemoveLabels: []string{"create-spec"}}

	if err := (&wfSideEffects{d: d}).ApplyHook(context.Background(), task, bindings, hook); err != nil {
		t.Fatalf("ApplyHook on a source without LabelRemover must be a no-op, got: %v", err)
	}
}
