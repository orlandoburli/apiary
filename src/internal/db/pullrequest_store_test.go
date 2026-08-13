package db

import (
	"context"
	"testing"
)

func TestTaskPullRequests_ReplaceAndList(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	// Initial set of two PRs, oldest first.
	in := []TaskPullRequest{
		{SourceID: "github", PRNumber: 10, PRURL: "https://gh/pr/10"},
		{SourceID: "github", PRNumber: 11, PRURL: "https://gh/pr/11"},
	}
	if err := c.ReplaceTaskPullRequests(ctx, "task1", "github", in); err != nil {
		t.Fatalf("replace: %v", err)
	}

	got, err := c.ListTaskPullRequests(ctx, "task1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	if got[0].PRNumber != 10 || got[1].PRNumber != 11 {
		t.Errorf("order = [%d, %d], want [10, 11]", got[0].PRNumber, got[1].PRNumber)
	}
	if got[0].Seq != 0 || got[1].Seq != 1 {
		t.Errorf("seq = [%d, %d], want [0, 1]", got[0].Seq, got[1].Seq)
	}
	// The tail is the most recent PR — what the (p) shortcut opens.
	if last := got[len(got)-1]; last.PRNumber != 11 {
		t.Errorf("tail = %d, want 11 (most recent)", last.PRNumber)
	}
}

func TestTaskPullRequests_ReplaceShrinksAndDropsStale(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	first := []TaskPullRequest{
		{SourceID: "github", PRNumber: 1, PRURL: "https://gh/pr/1"},
		{SourceID: "github", PRNumber: 2, PRURL: "https://gh/pr/2"},
		{SourceID: "github", PRNumber: 3, PRURL: "https://gh/pr/3"},
	}
	if err := c.ReplaceTaskPullRequests(ctx, "task1", "github", first); err != nil {
		t.Fatalf("replace 1: %v", err)
	}
	// Replace with a single PR — the stale rows must be gone.
	if err := c.ReplaceTaskPullRequests(ctx, "task1", "github",
		[]TaskPullRequest{{SourceID: "github", PRNumber: 3, PRURL: "https://gh/pr/3"}}); err != nil {
		t.Fatalf("replace 2: %v", err)
	}
	got, err := c.ListTaskPullRequests(ctx, "task1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].PRNumber != 3 || got[0].Seq != 0 {
		t.Fatalf("after shrink got %+v, want single PR #3 at seq 0", got)
	}
}

func TestTaskPullRequests_PerSourceIsolation(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	if err := c.ReplaceTaskPullRequests(ctx, "task1", "github",
		[]TaskPullRequest{{SourceID: "github", PRNumber: 1, PRURL: "https://gh/pr/1"}}); err != nil {
		t.Fatalf("replace github: %v", err)
	}
	if err := c.ReplaceTaskPullRequests(ctx, "task1", "gitlab",
		[]TaskPullRequest{{SourceID: "gitlab", PRNumber: 9, PRURL: "https://gl/mr/9"}}); err != nil {
		t.Fatalf("replace gitlab: %v", err)
	}
	// Replacing one source must not touch the other's rows.
	got, err := c.ListTaskPullRequests(ctx, "task1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2 (one per source)", len(got))
	}
}

// A workflow step reporting the PR it opened links it additively, and a
// re-reported PR is refreshed rather than duplicated (#425).
func TestTaskPullRequests_UpsertIsAdditiveAndIdempotent(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	if err := c.UpsertTaskPullRequest(ctx, "task1",
		TaskPullRequest{SourceID: "jira", PRNumber: 42, PRURL: "https://gh/pr/42"}); err != nil {
		t.Fatalf("upsert 1: %v", err)
	}
	// Same PR again (a loop-back re-ran the step): refreshed in place.
	if err := c.UpsertTaskPullRequest(ctx, "task1",
		TaskPullRequest{SourceID: "jira", PRNumber: 42, PRURL: "https://gh/pr/42", PRState: "open"}); err != nil {
		t.Fatalf("upsert 2: %v", err)
	}
	// A second, different PR on the same task.
	if err := c.UpsertTaskPullRequest(ctx, "task1",
		TaskPullRequest{SourceID: "jira", PRNumber: 43, PRURL: "https://gh/pr/43"}); err != nil {
		t.Fatalf("upsert 3: %v", err)
	}

	got, err := c.ListTaskPullRequests(ctx, "task1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2 (the re-reported PR must not duplicate): %+v", len(got), got)
	}
	if got[0].PRNumber != 42 || got[0].PRState != "open" {
		t.Errorf("first row = %+v, want PR 42 in state open", got[0])
	}
	// The newest link is the tail — what the dashboard's (p) shortcut opens.
	if last := got[len(got)-1]; last.PRNumber != 43 {
		t.Errorf("tail = %d, want 43", last.PRNumber)
	}
}

// Upserted links coexist with a source listing's rows for another source.
func TestTaskPullRequests_UpsertDoesNotDisturbOtherSources(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	if err := c.ReplaceTaskPullRequests(ctx, "task1", "github",
		[]TaskPullRequest{{SourceID: "github", PRNumber: 1, PRURL: "https://gh/pr/1"}}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if err := c.UpsertTaskPullRequest(ctx, "task1",
		TaskPullRequest{SourceID: "jira", PRNumber: 42, PRURL: "https://gh/pr/42"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := c.ListTaskPullRequests(ctx, "task1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(got), got)
	}
}
