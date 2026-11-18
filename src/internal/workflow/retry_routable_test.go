package workflow

import (
	"context"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
)

// A task must not be told a retry is pending when no retry will come.
//
// The failure count alone is not enough to decide that. A workflow's own
// on_fail hook commonly adds a label that its trigger excludes — every
// workflow adds needs-attention on failure, and every trigger excludes it — so
// the item stops matching the moment the first attempt fails. The failure
// count was still under the cap, so the task was marked blocked/retry_backoff:
// a claim about the future the system could not keep, and hundreds of tasks
// sat there with nothing coming.
//
// The task must settle as 'failed' instead — which is what it means: no human
// is coming, and no retry will either.
func TestSettle_RetryPendingNeedsTheTaskToStillBeRoutable(t *testing.T) {
	cases := []struct {
		name         string
		labels       []string
		onFailAdds   []string
		excluded     []string
		excludedPref string
		wantState    model.TaskState
		wantReason   string
	}{
		{
			name:       "still routable: a retry is pending",
			labels:     []string{"workflow:implementation"},
			excluded:   []string{"needs-human", "needs-attention"},
			wantState:  model.TaskStateBlocked,
			wantReason: "retry_backoff",
		},
		{
			name:       "the on_fail hook adds a label its own trigger excludes",
			labels:     []string{"workflow:implementation"},
			onFailAdds: []string{"needs-attention"},
			excluded:   []string{"needs-human", "needs-attention"},
			wantState:  model.TaskStateFailed,
		},
		{
			name:      "the task already carries an excluded label",
			labels:    []string{"workflow:implementation", "needs-attention"},
			excluded:  []string{"needs-human", "needs-attention"},
			wantState: model.TaskStateFailed,
		},
		{
			name:         "the task carries a label matching an excluded prefix",
			labels:       []string{"workflow:implementation", "agent:engineer"},
			excludedPref: "agent:",
			wantState:    model.TaskStateFailed,
		},
		{
			name:       "an excluded label that is not present does not block",
			labels:     []string{"workflow:implementation"},
			excluded:   []string{"draft", "needs-human"},
			wantState:  model.TaskStateBlocked,
			wantReason: "retry_backoff",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := newFakeTracker()
			tr.outstanding["T1"] = 1
			tr.consecutiveFailed = 1

			cfg := taskHookCfg()
			cfg.Settings.MaxAttempts = 3
			e := trackerEngine(cfg, newFakeStore(), &fakeExecutor{results: map[string]StepResult{}}, &fakeSide{}, tr)

			wf := config.WorkflowConfig{
				ID: "wf",
				Trigger: &config.TriggerConfig{
					Match: config.RouteMatch{
						Labels:             []string{"workflow:implementation"},
						ExcludeLabels:      tc.excluded,
						ExcludeLabelPrefix: tc.excludedPref,
					},
				},
			}
			if len(tc.onFailAdds) > 0 {
				wf.OnFail = &config.OnComplete{AddLabels: tc.onFailAdds}
			}

			r := &dagRun{
				instID: "i1",
				task: model.InternalTask{
					ID:       "T1",
					Metadata: model.TaskMetadata{Labels: tc.labels},
				},
				wf: wf,
			}
			e.completeTask(context.Background(), r, true)

			if got := tr.states["T1"]; got != tc.wantState {
				t.Errorf("task state = %q, want %q", got, tc.wantState)
			}
			if got := tr.reasons["T1"]; got != tc.wantReason {
				t.Errorf("blocked reason = %q, want %q", got, tc.wantReason)
			}
		})
	}
}

// A workflow with no trigger of its own is not routed by a poll, so the
// routability check cannot say it is not routable: a sub-workflow dispatched
// by its parent, or a manually run one, must still be labelled as awaiting a
// retry.
func TestSettle_RetryPendingForATriggerlessWorkflow(t *testing.T) {
	tr := newFakeTracker()
	tr.outstanding["T1"] = 1
	tr.consecutiveFailed = 1

	cfg := taskHookCfg()
	cfg.Settings.MaxAttempts = 3
	e := trackerEngine(cfg, newFakeStore(), &fakeExecutor{results: map[string]StepResult{}}, &fakeSide{}, tr)

	r := &dagRun{
		instID: "i1",
		task: model.InternalTask{
			ID:       "T1",
			Metadata: model.TaskMetadata{Labels: []string{"needs-attention"}},
		},
		wf: config.WorkflowConfig{ID: "wf"},
	}
	e.completeTask(context.Background(), r, true)

	if got := tr.states["T1"]; got != model.TaskStateBlocked {
		t.Errorf("task state = %q, want %q — a triggerless workflow is not routed by a poll", got, model.TaskStateBlocked)
	}
	if got := tr.reasons["T1"]; got != "retry_backoff" {
		t.Errorf("blocked reason = %q, want retry_backoff", got)
	}
}
