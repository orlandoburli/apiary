package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/source"
)

// prCIServer stands up a GitHub API stub for PR 1961 (head SHA "abc") addressed
// DIRECTLY by number — no issue and no timeline, which is exactly the situation
// of a Jira-sourced task: nothing in this repo cross-references the work item.
func prCIServer(t *testing.T, combinedStatus, checkRuns string) *Adapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/timeline"), strings.Contains(r.URL.Path, "/issues/"):
			t.Errorf("issue endpoint %q hit: a PR-addressed check must not need the issue", r.URL.Path)
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, "/pulls/1961"):
			_, _ = w.Write([]byte(`{"number":1961,"html_url":"https://gh/pr/1961","head":{"sha":"abc"}}`))
		case strings.HasSuffix(r.URL.Path, "/commits/abc/status"):
			_, _ = w.Write([]byte(combinedStatus))
		case strings.HasSuffix(r.URL.Path, "/commits/abc/check-runs"):
			_, _ = w.Write([]byte(checkRuns))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &Adapter{id: "gh", owner: "o", repo: "r", client: newClient(srv.URL, "")}
}

// The whole point of #444: CI for a PR the adapter's own source never saw.
func TestPollCIStatusForPR_GreenChecks(t *testing.T) {
	a := prCIServer(t, `{"state":"pending","total_count":0,"statuses":[]}`,
		`{"check_runs":[{"name":"test","status":"completed","conclusion":"success"}]}`)

	got, err := a.PollCIStatusForPR(context.Background(),
		source.PullRequestRef{Number: 1961, URL: "https://github.com/o/r/pull/1961"})
	if err != nil {
		t.Fatalf("PollCIStatusForPR: %v", err)
	}
	if got.Status != "passed" {
		t.Errorf("status = %q, want passed", got.Status)
	}
	if got.URL != "https://gh/pr/1961" {
		t.Errorf("url = %q, want the PR html_url", got.URL)
	}
}

// The conflict short-circuit is shared with the issue-addressed path, so it must
// hold here too — a conflicting PR never becomes "pending forever".
func TestPollCIStatusForPR_MergeConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/pulls/1961") {
			_, _ = w.Write([]byte(`{"number":1961,"html_url":"https://gh/pr/1961","head":{"sha":"abc"},"mergeable_state":"dirty"}`))
			return
		}
		t.Errorf("unexpected request %q after a dirty PR", r.URL.Path)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	a := &Adapter{id: "gh", owner: "o", repo: "r", client: newClient(srv.URL, "")}

	got, err := a.PollCIStatusForPR(context.Background(), source.PullRequestRef{Number: 1961})
	if err != nil {
		t.Fatalf("PollCIStatusForPR: %v", err)
	}
	if got.Status != "conflict" {
		t.Errorf("status = %q, want conflict", got.Status)
	}
}

// A PR from another repository must be refused, not answered with this repo's
// same-numbered PR — a wrong green light is worse than an error.
func TestPollCIStatusForPR_RejectsForeignRepo(t *testing.T) {
	a := prCIServer(t, `{}`, `{}`)

	_, err := a.PollCIStatusForPR(context.Background(),
		source.PullRequestRef{Number: 1961, URL: "https://github.com/other/project/pull/1961"})
	if err == nil {
		t.Fatal("PollCIStatusForPR on a foreign PR: want error, got nil")
	}
	if !errors.Is(err, source.ErrUnsupported) {
		t.Errorf("error %v does not wrap ErrUnsupported — the engine would retry a permanent misconfiguration", err)
	}
}

// A ref carrying no URL is trusted: the number is all the caller had.
func TestPollCIStatusForPR_NoURLIsAccepted(t *testing.T) {
	a := prCIServer(t, `{"state":"pending","total_count":0,"statuses":[]}`,
		`{"check_runs":[{"name":"test","status":"in_progress"}]}`)

	got, err := a.PollCIStatusForPR(context.Background(), source.PullRequestRef{Number: 1961})
	if err != nil {
		t.Fatalf("PollCIStatusForPR: %v", err)
	}
	if got.Status != "pending" {
		t.Errorf("status = %q, want pending", got.Status)
	}
}

func TestPollCIStatusForPR_RejectsZeroNumber(t *testing.T) {
	a := prCIServer(t, `{}`, `{}`)
	if _, err := a.PollCIStatusForPR(context.Background(), source.PullRequestRef{}); err == nil {
		t.Fatal("want error for a ref with no PR number")
	}
}
