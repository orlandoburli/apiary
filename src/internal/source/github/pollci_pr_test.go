package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// ciReposServer stands up a GitHub API stub serving PR 7 of
// orlandoburli/apiary, for an adapter configured for orlandoburli/project-erp
// that allows apiary through ci_repos. Every request path is recorded so a test
// can prove the CI calls followed the PR to its OWN repository.
func ciReposServer(t *testing.T, webBaseURL, prBody, combinedStatus, checkRuns string) (*Adapter, *[]string) {
	t.Helper()
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls/7"):
			_, _ = w.Write([]byte(prBody))
		case strings.HasSuffix(r.URL.Path, "/status"):
			_, _ = w.Write([]byte(combinedStatus))
		case strings.HasSuffix(r.URL.Path, "/check-runs"):
			_, _ = w.Write([]byte(checkRuns))
		default:
			t.Errorf("unexpected request %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	return &Adapter{
		id:         "gh",
		owner:      "orlandoburli",
		repo:       "project-erp",
		webBaseURL: webBaseURL,
		client:     newClient(srv.URL, ""),
		ciRepos: []ciRepoPolicy{{
			owner: "orlandoburli", repo: "apiary",
			noChecksAfter: 10 * time.Minute,
		}},
	}, &paths
}

// prBodyAged returns PR 7's payload with created_at the given age, so the
// no_checks grace period can be exercised without a fake clock.
func prBodyAged(age time.Duration) string {
	created := time.Now().Add(-age).Format(time.RFC3339)
	return `{"number":7,"html_url":"https://github.com/orlandoburli/apiary/pull/7","head":{"sha":"abc"},"created_at":"` + created + `"}`
}

const zeroSignals = `{"state":"pending","total_count":0,"statuses":[]}`

// A PR of a repository declared in ci_repos is polled in ITS repository, not in
// the source's own — the whole point of #8151.
func TestPollCIStatusForPR_AllowedCIRepoPollsThatRepo(t *testing.T) {
	a, paths := ciReposServer(t, "https://github.com",
		prBodyAged(time.Hour),
		zeroSignals,
		`{"check_runs":[{"name":"test","status":"completed","conclusion":"success"}]}`)

	got, err := a.PollCIStatusForPR(context.Background(),
		source.PullRequestRef{Number: 7, URL: "https://github.com/orlandoburli/apiary/pull/7"})
	if err != nil {
		t.Fatalf("PollCIStatusForPR: %v", err)
	}
	if got.Status != "passed" {
		t.Errorf("status = %q, want passed", got.Status)
	}
	if got.URL != "https://github.com/orlandoburli/apiary/pull/7" {
		t.Errorf("url = %q, want the PR html_url", got.URL)
	}

	want := []string{
		"/repos/orlandoburli/apiary/pulls/7",
		"/repos/orlandoburli/apiary/commits/abc/status",
		"/repos/orlandoburli/apiary/commits/abc/check-runs",
	}
	if len(*paths) != len(want) {
		t.Fatalf("requests = %v, want %v", *paths, want)
	}
	for i := range want {
		if (*paths)[i] != want[i] {
			t.Errorf("request %d = %q, want %q", i, (*paths)[i], want[i])
		}
	}
}

// A PR URL in API form names the same repository; it must resolve like the
// browser form, not be refused for looking different.
func TestPollCIStatusForPR_AllowedCIRepoInAPIForm(t *testing.T) {
	// A source whose base_url is the API host itself: the host check then allows
	// api.github.com, which is the host an API-form URL carries.
	a, _ := ciReposServer(t, "https://api.github.com",
		prBodyAged(time.Hour),
		zeroSignals,
		`{"check_runs":[{"name":"test","status":"completed","conclusion":"success"}]}`)

	got, err := a.PollCIStatusForPR(context.Background(),
		source.PullRequestRef{Number: 7, URL: "https://api.github.com/repos/orlandoburli/apiary/pulls/7"})
	if err != nil {
		t.Fatalf("PollCIStatusForPR: %v", err)
	}
	if got.Status != "passed" {
		t.Errorf("status = %q, want passed", got.Status)
	}
}

// A repository that is neither the source's own nor in ci_repos is refused, and
// the error names the allow-list so the fix is obvious from the log.
func TestPollCIStatusForPR_RejectsRepoOutsideCIRepos(t *testing.T) {
	a, paths := ciReposServer(t, "https://github.com", prBodyAged(time.Hour), zeroSignals, `{}`)

	_, err := a.PollCIStatusForPR(context.Background(),
		source.PullRequestRef{Number: 7, URL: "https://github.com/someone/else/pull/7"})
	if err == nil {
		t.Fatal("PollCIStatusForPR on a repo outside ci_repos: want error, got nil")
	}
	if !errors.Is(err, source.ErrUnsupported) {
		t.Errorf("error %v does not wrap ErrUnsupported", err)
	}
	for _, want := range []string{"ci_repos", "orlandoburli/apiary", "someone/else"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
	if len(*paths) != 0 {
		t.Errorf("requests = %v, want none — a refused ref must not touch the API", *paths)
	}
}

// A URL served by another forge is not a PR this source can answer for, ci_repos
// or not.
func TestPollCIStatusForPR_RejectsForeignHost(t *testing.T) {
	a, _ := ciReposServer(t, "https://github.com", prBodyAged(time.Hour), zeroSignals, `{}`)

	_, err := a.PollCIStatusForPR(context.Background(),
		source.PullRequestRef{Number: 7, URL: "https://gitlab.com/orlandoburli/apiary/pull/7"})
	if err == nil {
		t.Fatal("PollCIStatusForPR on a foreign host: want error, got nil")
	}
	if !errors.Is(err, source.ErrUnsupported) {
		t.Errorf("error %v does not wrap ErrUnsupported", err)
	}
	if !strings.Contains(err.Error(), "gitlab.com") {
		t.Errorf("error %q does not name the foreign host", err)
	}
}

// With require_checks: false and no CI signal at all, an old PR concludes
// no_checks: the repository simply does not report CI for its PRs.
func TestPollCIStatusForPR_NoSignalsPastGraceIsNoChecks(t *testing.T) {
	a, _ := ciReposServer(t, "https://github.com", prBodyAged(15*time.Minute),
		zeroSignals, `{"check_runs":[]}`)

	got, err := a.PollCIStatusForPR(context.Background(),
		source.PullRequestRef{Number: 7, URL: "https://github.com/orlandoburli/apiary/pull/7"})
	if err != nil {
		t.Fatalf("PollCIStatusForPR: %v", err)
	}
	if got.Status != "no_checks" {
		t.Errorf("status = %q, want no_checks", got.Status)
	}
	if got.URL != "https://github.com/orlandoburli/apiary/pull/7" {
		t.Errorf("url = %q, want the PR html_url", got.URL)
	}
}

// Inside the grace period the same PR is still pending: CI may simply not have
// registered its first check yet.
func TestPollCIStatusForPR_NoSignalsWithinGraceIsPending(t *testing.T) {
	a, _ := ciReposServer(t, "https://github.com", prBodyAged(time.Minute),
		zeroSignals, `{"check_runs":[]}`)

	got, err := a.PollCIStatusForPR(context.Background(),
		source.PullRequestRef{Number: 7, URL: "https://github.com/orlandoburli/apiary/pull/7"})
	if err != nil {
		t.Fatalf("PollCIStatusForPR: %v", err)
	}
	if got.Status != "pending" {
		t.Errorf("status = %q, want pending", got.Status)
	}
}

// The source's own repository never relaxes to no_checks, however old the PR:
// its gate must keep failing a PR whose CI never ran.
func TestPollCIStatusForPR_OwnRepoOldPRStaysPending(t *testing.T) {
	a, _ := ciReposServer(t, "https://github.com", prBodyAged(6*time.Hour),
		zeroSignals, `{"check_runs":[]}`)

	got, err := a.PollCIStatusForPR(context.Background(),
		source.PullRequestRef{Number: 7, URL: "https://github.com/orlandoburli/project-erp/pull/7"})
	if err != nil {
		t.Fatalf("PollCIStatusForPR: %v", err)
	}
	if got.Status != "pending" {
		t.Errorf("status = %q, want pending — the own repository always requires checks", got.Status)
	}
}

// require_checks: false relaxes only the zero-signal case; a PR that does report
// CI aggregates exactly as it would in the source's own repository.
func TestPollCIStatusForPR_SignalsWithLenientPolicyAggregate(t *testing.T) {
	cases := map[string]struct {
		checks string
		want   string
	}{
		"green": {`{"check_runs":[{"name":"test","status":"completed","conclusion":"success"}]}`, "passed"},
		"red":   {`{"check_runs":[{"name":"test","status":"completed","conclusion":"failure"}]}`, "failed"},
		"runs":  {`{"check_runs":[{"name":"test","status":"in_progress"}]}`, "pending"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// An old PR: only the presence of a signal keeps it out of no_checks.
			a, _ := ciReposServer(t, "https://github.com", prBodyAged(time.Hour), zeroSignals, tc.checks)

			got, err := a.PollCIStatusForPR(context.Background(),
				source.PullRequestRef{Number: 7, URL: "https://github.com/orlandoburli/apiary/pull/7"})
			if err != nil {
				t.Fatalf("PollCIStatusForPR: %v", err)
			}
			if got.Status != tc.want {
				t.Errorf("status = %q, want %q", got.Status, tc.want)
			}
		})
	}
}
