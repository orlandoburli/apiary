package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/source"
)

// prListerAdapter is a minimal source.Adapter that also implements
// source.PullRequestLister, returning a scripted result (or error).
type prListerAdapter struct {
	prs []source.PullRequestRef
	err error
}

func (a *prListerAdapter) ID() string                                    { return "fake" }
func (a *prListerAdapter) Connect(context.Context, map[string]any) error { return nil }
func (a *prListerAdapter) Poll(context.Context, time.Time) ([]model.SourceItem, error) {
	return nil, nil
}
func (a *prListerAdapter) Acknowledge(context.Context, model.SourceItem, model.AckAction) error {
	return nil
}
func (a *prListerAdapter) WriteResult(context.Context, model.SourceItem, model.RunResult) error {
	return nil
}

func (a *prListerAdapter) ListPullRequests(context.Context, string) ([]source.PullRequestRef, error) {
	return a.prs, a.err
}

func newPRTestClient(t *testing.T) *db.Client {
	t.Helper()
	c, err := db.New(context.Background(), filepath.Join(t.TempDir(), "pulls.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestRefreshTaskPullRequests_PersistsAndReturns(t *testing.T) {
	ctx := context.Background()
	c := newPRTestClient(t)
	taskID, _ := seedBoundTask(ctx, t, c, "github", "42")

	adapter := &prListerAdapter{prs: []source.PullRequestRef{
		{Number: 7, URL: "https://gh/pr/7"},
		{Number: 9, URL: "https://gh/pr/9"},
	}}
	d := &Dispatcher{db: c, sources: map[string]source.Adapter{"github": adapter}}

	resp, err := d.RefreshTaskPullRequests(ctx, taskID)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if len(resp.Pulls) != 2 || resp.Pulls[len(resp.Pulls)-1].Number != 9 {
		t.Fatalf("got %+v, want 2 PRs with #9 last", resp.Pulls)
	}
	// Persisted, so a later read (e.g. the dashboard) sees the same set.
	stored, _ := c.ListTaskPullRequests(ctx, taskID)
	if len(stored) != 2 {
		t.Fatalf("persisted %d rows, want 2", len(stored))
	}
}

func TestRefreshTaskPullRequests_ListerErrorKeepsLastGood(t *testing.T) {
	ctx := context.Background()
	c := newPRTestClient(t)
	taskID, _ := seedBoundTask(ctx, t, c, "github", "42")

	// Seed a last-good set directly.
	if err := c.ReplaceTaskPullRequests(ctx, taskID, "github",
		[]db.TaskPullRequest{{SourceID: "github", PRNumber: 1, PRURL: "https://gh/pr/1"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	adapter := &prListerAdapter{err: errors.New("boom")}
	d := &Dispatcher{db: c, sources: map[string]source.Adapter{"github": adapter}}

	resp, err := d.RefreshTaskPullRequests(ctx, taskID)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	// The transient error must not wipe the persisted rows.
	if len(resp.Pulls) != 1 || resp.Pulls[0].Number != 1 {
		t.Fatalf("got %+v, want the last-good PR #1 preserved", resp.Pulls)
	}
}

func TestRefreshTaskPullRequests_NonListerSourceNoop(t *testing.T) {
	ctx := context.Background()
	c := newPRTestClient(t)
	taskID, _ := seedBoundTask(ctx, t, c, "github", "42")

	d := &Dispatcher{db: c, sources: map[string]source.Adapter{"github": plainAdapterNoLister{}}}
	resp, err := d.RefreshTaskPullRequests(ctx, taskID)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if len(resp.Pulls) != 0 {
		t.Fatalf("got %+v, want none (source can't list PRs)", resp.Pulls)
	}
}

func TestRefreshTaskPullRequests_UnknownTask(t *testing.T) {
	c := newPRTestClient(t)
	d := &Dispatcher{db: c, sources: map[string]source.Adapter{}}
	if _, err := d.RefreshTaskPullRequests(context.Background(), "nope"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("err = %v, want ErrTaskNotFound", err)
	}
}

// plainAdapterNoLister implements source.Adapter but NOT source.PullRequestLister.
type plainAdapterNoLister struct{}

func (plainAdapterNoLister) ID() string                                    { return "plain" }
func (plainAdapterNoLister) Connect(context.Context, map[string]any) error { return nil }
func (plainAdapterNoLister) Poll(context.Context, time.Time) ([]model.SourceItem, error) {
	return nil, nil
}
func (plainAdapterNoLister) Acknowledge(context.Context, model.SourceItem, model.AckAction) error {
	return nil
}
func (plainAdapterNoLister) WriteResult(context.Context, model.SourceItem, model.RunResult) error {
	return nil
}
