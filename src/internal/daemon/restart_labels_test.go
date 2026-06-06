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

func restartTestConfig() *config.Config {
	return &config.Config{
		Sources: []config.SourceConfig{{ID: "fake"}},
		Workflows: []config.WorkflowConfig{
			{ID: "eng", Trigger: &config.TriggerConfig{Match: config.RouteMatch{
				Labels:        []string{"agent:engineer"},
				ExcludeLabels: []string{"in-progress"},
			}}, Steps: []config.StepConfig{{ID: "run", Agent: "engineer"}}},
			{ID: "classify", Trigger: &config.TriggerConfig{Match: config.RouteMatch{ExcludeLabelPrefix: "agent:"}},
				Steps: []config.StepConfig{{ID: "run", Agent: "investigator"}}},
		},
	}
}

func TestControlLabels_DerivesFromRouteExclusions(t *testing.T) {
	d := &Dispatcher{cfg: restartTestConfig()}
	cell := model.SourceItem{Labels: []string{"agent:engineer", "in-progress", "bug", "area:backend"}}

	got := d.controlLabels(cell)

	want := map[string]bool{"agent:engineer": true, "in-progress": true}
	if len(got) != len(want) {
		t.Fatalf("controlLabels = %v, want exactly %v", got, []string{"agent:engineer", "in-progress"})
	}
	for _, l := range got {
		if !want[l] {
			t.Errorf("unexpected control label %q (non-control labels must be kept)", l)
		}
	}
}

func TestControlLabels_NoneWhenOnlyPlainLabels(t *testing.T) {
	d := &Dispatcher{cfg: restartTestConfig()}
	cell := model.SourceItem{Labels: []string{"bug", "area:backend", "prioridade:alta"}}
	if got := d.controlLabels(cell); len(got) != 0 {
		t.Errorf("controlLabels = %v, want none", got)
	}
}

// fakeRestartSource implements the Adapter interface plus the optional
// TaskPoller, LabelRemover, and StateSetter interfaces ForceRestart relies on.
type fakeRestartSource struct {
	cell     model.SourceItem
	removed  []string
	stateSet string
}

func (f *fakeRestartSource) ID() string                                    { return "fake" }
func (f *fakeRestartSource) Connect(context.Context, map[string]any) error { return nil }
func (f *fakeRestartSource) Poll(context.Context, time.Time) ([]model.SourceItem, error) {
	return nil, nil
}
func (f *fakeRestartSource) Acknowledge(context.Context, model.SourceItem, model.AckAction) error {
	return nil
}
func (f *fakeRestartSource) WriteResult(context.Context, model.SourceItem, model.RunResult) error {
	return nil
}
func (f *fakeRestartSource) WebhookHandler() http.Handler { return nil }
func (f *fakeRestartSource) PollTask(context.Context, string) (model.SourceItem, error) {
	return f.cell, nil
}
func (f *fakeRestartSource) RemoveLabels(_ context.Context, _ model.SourceItem, labels []string) error {
	f.removed = append(f.removed, labels...)
	return nil
}
func (f *fakeRestartSource) SetState(_ context.Context, _ model.SourceItem, state string) error {
	f.stateSet = state
	return nil
}

func TestForceRestart_StripsControlLabels(t *testing.T) {
	fake := &fakeRestartSource{cell: model.SourceItem{
		ID:     "42",
		Labels: []string{"agent:engineer", "in-progress", "bug"},
	}}
	d := &Dispatcher{
		cfg:     restartTestConfig(),
		sources: map[string]source.Adapter{"fake": fake},
	}

	if err := d.ForceRestart(context.Background(), "42"); err != nil {
		t.Fatalf("ForceRestart: %v", err)
	}

	got := map[string]bool{}
	for _, l := range fake.removed {
		got[l] = true
	}
	if !got["agent:engineer"] || !got["in-progress"] {
		t.Errorf("removed = %v, want both agent:engineer and in-progress", fake.removed)
	}
	if got["bug"] {
		t.Errorf("removed non-control label 'bug': %v", fake.removed)
	}
	if fake.stateSet != "todo" {
		t.Errorf("stateSet = %q, want todo", fake.stateSet)
	}
}
