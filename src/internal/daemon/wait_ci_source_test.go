package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/source"
	"github.com/orlandoburli/apiary/internal/workflow"
)

// forgeAdapter is a git forge that can answer for a pull request by number but
// knows nothing about the task's own source items — the GitHub side of a
// Jira-sourced pipeline (#444).
type forgeAdapter struct {
	mu   sync.Mutex
	seen []source.PullRequestRef
}

func (a *forgeAdapter) ID() string                                                  { return "github" }
func (a *forgeAdapter) Connect(context.Context, map[string]any) error               { return nil }
func (a *forgeAdapter) Poll(context.Context, time.Time) ([]model.SourceItem, error) { return nil, nil }
func (a *forgeAdapter) Acknowledge(context.Context, model.SourceItem, model.AckAction) error {
	return nil
}
func (a *forgeAdapter) WriteResult(context.Context, model.SourceItem, model.RunResult) error {
	return nil
}
func (a *forgeAdapter) PollCIStatusForPR(_ context.Context, pr source.PullRequestRef) (source.CIStatus, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seen = append(a.seen, pr)
	return source.CIStatus{Status: "passed", URL: pr.URL}, nil
}

// trackerAdapter owns the work items and has no CI capability at all (Jira).
type trackerAdapter struct{}

func (trackerAdapter) ID() string                                                  { return "jira" }
func (trackerAdapter) Connect(context.Context, map[string]any) error               { return nil }
func (trackerAdapter) Poll(context.Context, time.Time) ([]model.SourceItem, error) { return nil, nil }
func (trackerAdapter) Acknowledge(context.Context, model.SourceItem, model.AckAction) error {
	return nil
}
func (trackerAdapter) WriteResult(context.Context, model.SourceItem, model.RunResult) error {
	return nil
}

func ciSourceDispatcher(t *testing.T) (*Dispatcher, *db.Client, *forgeAdapter) {
	t.Helper()
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "ci.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	forge := &forgeAdapter{}
	d := &Dispatcher{
		db:      dbc,
		sources: map[string]source.Adapter{"jira": trackerAdapter{}, "github": forge},
	}
	d.binder = source.NewSourceBinder(dbc)
	return d, dbc, forge
}

// The happy path: the CI status of the PR linked to a Jira-sourced task comes
// from the forge named by ci_source, addressed by PR number.
func TestPollCIForLinkedPR_UsesNewestLinkedPR(t *testing.T) {
	ctx := context.Background()
	d, dbc, forge := ciSourceDispatcher(t)

	task, err := d.binder.Bind(ctx, model.SourceItem{ID: "PSP-1", SourceID: "jira", Title: "PSP-1"})
	if err != nil {
		t.Fatalf("bind task: %v", err)
	}
	for _, pr := range []db.TaskPullRequest{
		{SourceID: "jira", PRNumber: 10, PRURL: "https://github.com/o/r/pull/10", PRState: "closed"},
		{SourceID: "jira", PRNumber: 11, PRURL: "https://github.com/o/r/pull/11", PRState: "open"},
	} {
		if err := dbc.UpsertTaskPullRequest(ctx, task.ID, pr); err != nil {
			t.Fatalf("link PR: %v", err)
		}
	}

	got, err := d.pollCIForLinkedPR(ctx, workflow.CIStatusRequest{
		TaskID: task.ID, SourceID: "jira", SourceItemID: "PSP-1", CISourceID: "github",
	})
	if err != nil {
		t.Fatalf("pollCIForLinkedPR: %v", err)
	}
	if got.Status != "passed" {
		t.Errorf("status = %q, want passed", got.Status)
	}
	if len(forge.seen) != 1 || forge.seen[0].Number != 11 {
		t.Errorf("forge polled %+v, want only the newest linked PR (#11) — a rework lap must not wait on the old PR", forge.seen)
	}
}

// No PR reported yet is a "not yet", so the wait stays parked instead of failing.
func TestPollCIForLinkedPR_NoLinkedPR(t *testing.T) {
	ctx := context.Background()
	d, _, _ := ciSourceDispatcher(t)

	_, err := d.pollCIForLinkedPR(ctx, workflow.CIStatusRequest{TaskID: "t1", CISourceID: "github"})
	if !errors.Is(err, workflow.ErrPRNotLinked) {
		t.Errorf("error = %v, want ErrPRNotLinked so the wait keeps polling", err)
	}
}

// A ci_source that is not configured, or cannot poll a PR, is permanent: the
// error must wrap ErrUnsupported so the engine fails the step at once.
func TestPollCIForLinkedPR_UnsupportedCISource(t *testing.T) {
	ctx := context.Background()
	d, _, _ := ciSourceDispatcher(t)

	for name, req := range map[string]workflow.CIStatusRequest{
		"unknown source":     {TaskID: "t1", CISourceID: "gitlab"},
		"source without cap": {TaskID: "t1", CISourceID: "jira"},
	} {
		if _, err := d.pollCIForLinkedPR(ctx, req); !errors.Is(err, source.ErrUnsupported) {
			t.Errorf("%s: error = %v, want it to wrap ErrUnsupported", name, err)
		}
	}
}
