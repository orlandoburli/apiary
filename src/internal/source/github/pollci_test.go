package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ciServer stands up a GitHub API stub for an issue (1958) whose PR (1961, head
// SHA "abc") reports the given combined-status JSON and check-runs JSON.
func ciServer(t *testing.T, combinedStatus, checkRuns string) *Adapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls/1958"):
			http.NotFound(w, r) // issue number is not a PR
		case strings.HasSuffix(r.URL.Path, "/issues/1958/timeline"):
			_, _ = w.Write([]byte(`[{"event":"cross-referenced","source":{"type":"issue",
				"issue":{"number":1961,"pull_request":{"url":"https://api/pulls/1961"}}}}]`))
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

// conflictServer is like ciServer but lets the PR (1961) carry a mergeable_state,
// so we can exercise the merge-conflict short-circuit. It also fails the test if
// the CI endpoints are hit, proving a dirty PR never reaches CI aggregation.
func conflictServer(t *testing.T, mergeableState string) *Adapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls/1958"):
			http.NotFound(w, r) // issue number is not a PR
		case strings.HasSuffix(r.URL.Path, "/issues/1958/timeline"):
			_, _ = w.Write([]byte(`[{"event":"cross-referenced","source":{"type":"issue",
				"issue":{"number":1961,"pull_request":{"url":"https://api/pulls/1961"}}}}]`))
		case strings.HasSuffix(r.URL.Path, "/pulls/1961"):
			_, _ = w.Write([]byte(`{"number":1961,"html_url":"https://gh/pr/1961","head":{"sha":"abc"},"mergeable":false,"mergeable_state":"` + mergeableState + `"}`))
		case strings.Contains(r.URL.Path, "/commits/abc/"):
			t.Errorf("CI endpoint %q hit, but a conflicting PR should short-circuit before CI aggregation", r.URL.Path)
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &Adapter{id: "gh", owner: "o", repo: "r", client: newClient(srv.URL, "")}
}

// A PR with merge conflicts (mergeable_state="dirty") short-circuits to a
// "conflict" status without ever polling CI.
func TestPollCIStatus_MergeConflict(t *testing.T) {
	a := conflictServer(t, "dirty")

	got, err := a.PollCIStatus(context.Background(), "1958")
	if err != nil {
		t.Fatalf("PollCIStatus: %v", err)
	}
	if got.Status != "conflict" {
		t.Errorf("status = %q, want conflict (PR mergeable_state=dirty)", got.Status)
	}
	if got.URL != "https://gh/pr/1961" {
		t.Errorf("url = %q, want the PR html_url", got.URL)
	}
}

// mergeable_state values other than "dirty" (here GitHub still computing it) are
// NOT treated as conflicts — the step keeps waiting on CI as usual.
func TestPollCIStatus_UnknownMergeStateIsNotConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls/1958"):
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, "/issues/1958/timeline"):
			_, _ = w.Write([]byte(`[{"event":"cross-referenced","source":{"type":"issue",
				"issue":{"number":1961,"pull_request":{"url":"https://api/pulls/1961"}}}}]`))
		case strings.HasSuffix(r.URL.Path, "/pulls/1961"):
			_, _ = w.Write([]byte(`{"number":1961,"html_url":"https://gh/pr/1961","head":{"sha":"abc"},"mergeable":null,"mergeable_state":"unknown"}`))
		case strings.HasSuffix(r.URL.Path, "/commits/abc/status"):
			_, _ = w.Write([]byte(`{"state":"pending","total_count":0,"statuses":[]}`))
		case strings.HasSuffix(r.URL.Path, "/commits/abc/check-runs"):
			_, _ = w.Write([]byte(`{"check_runs":[{"name":"CI","status":"in_progress","conclusion":null}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	a := &Adapter{id: "gh", owner: "o", repo: "r", client: newClient(srv.URL, "")}

	got, err := a.PollCIStatus(context.Background(), "1958")
	if err != nil {
		t.Fatalf("PollCIStatus: %v", err)
	}
	if got.Status != "pending" {
		t.Errorf("status = %q, want pending (mergeable_state unknown ≠ conflict)", got.Status)
	}
}

// The regression: a GitHub-Actions-only repo has an EMPTY combined status (state
// defaults to "pending", total_count 0) while every check run is green. The
// overall must be "passed", not "pending".
func TestPollCIStatus_ActionsOnlyAllGreen(t *testing.T) {
	a := ciServer(t,
		`{"state":"pending","total_count":0,"statuses":[]}`,
		`{"check_runs":[
			{"name":"CI","status":"completed","conclusion":"success"},
			{"name":"E2E","status":"completed","conclusion":"success"},
			{"name":"optional","status":"completed","conclusion":"skipped"}
		]}`)

	got, err := a.PollCIStatus(context.Background(), "1958")
	if err != nil {
		t.Fatalf("PollCIStatus: %v", err)
	}
	if got.Status != "passed" {
		t.Errorf("status = %q, want passed (Actions-only repo, all checks green)", got.Status)
	}
}

func TestPollCIStatus_FailedCheckRun(t *testing.T) {
	a := ciServer(t,
		`{"state":"pending","total_count":0,"statuses":[]}`,
		`{"check_runs":[
			{"name":"CI","status":"completed","conclusion":"success"},
			{"name":"E2E","status":"completed","conclusion":"failure"}
		]}`)

	got, err := a.PollCIStatus(context.Background(), "1958")
	if err != nil {
		t.Fatalf("PollCIStatus: %v", err)
	}
	if got.Status != "failed" {
		t.Errorf("status = %q, want failed", got.Status)
	}
}

func TestPollCIStatus_StillRunning(t *testing.T) {
	a := ciServer(t,
		`{"state":"pending","total_count":0,"statuses":[]}`,
		`{"check_runs":[
			{"name":"CI","status":"completed","conclusion":"success"},
			{"name":"E2E","status":"in_progress","conclusion":null}
		]}`)

	got, err := a.PollCIStatus(context.Background(), "1958")
	if err != nil {
		t.Fatalf("PollCIStatus: %v", err)
	}
	if got.Status != "pending" {
		t.Errorf("status = %q, want pending (a check is still in_progress)", got.Status)
	}
}

func TestPollCIStatus_NoSignalsYet(t *testing.T) {
	a := ciServer(t,
		`{"state":"pending","total_count":0,"statuses":[]}`,
		`{"check_runs":[]}`)

	got, err := a.PollCIStatus(context.Background(), "1958")
	if err != nil {
		t.Fatalf("PollCIStatus: %v", err)
	}
	if got.Status != "pending" {
		t.Errorf("status = %q, want pending (CI not started)", got.Status)
	}
}

// The regression behind the 2026-07-05 needs-human cascade: the issue timeline
// cross-references BOTH a stale CLOSED PR with conflicts (from a sibling issue
// that merely mentioned this one) and the issue's real OPEN PR. The poller must
// pick the open PR — not short-circuit on the closed dirty one.
func TestPollCIStatus_PrefersOpenPROverStaleClosedOne(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls/1958"):
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, "/issues/1958/timeline"):
			// Chronological order: the real PR (1961) referenced first, then the
			// stale closed one (1960) referenced later (= most recent).
			_, _ = w.Write([]byte(`[
				{"event":"cross-referenced","source":{"type":"issue",
					"issue":{"number":1961,"pull_request":{"url":"https://api/pulls/1961"}}}},
				{"event":"cross-referenced","source":{"type":"issue",
					"issue":{"number":1960,"pull_request":{"url":"https://api/pulls/1960"}}}}]`))
		case strings.HasSuffix(r.URL.Path, "/pulls/1960"):
			_, _ = w.Write([]byte(`{"number":1960,"state":"closed","html_url":"https://gh/pr/1960","head":{"sha":"stale"},"mergeable":false,"mergeable_state":"dirty"}`))
		case strings.HasSuffix(r.URL.Path, "/pulls/1961"):
			_, _ = w.Write([]byte(`{"number":1961,"state":"open","html_url":"https://gh/pr/1961","head":{"sha":"abc"},"mergeable":true,"mergeable_state":"clean"}`))
		case strings.HasSuffix(r.URL.Path, "/commits/abc/status"):
			_, _ = w.Write([]byte(`{"state":"pending","total_count":0,"statuses":[]}`))
		case strings.HasSuffix(r.URL.Path, "/commits/abc/check-runs"):
			_, _ = w.Write([]byte(`{"check_runs":[{"name":"CI","status":"completed","conclusion":"success"}]}`))
		case strings.Contains(r.URL.Path, "/commits/stale/"):
			t.Errorf("CI polled on the stale closed PR's head %q — the open PR should have been picked", r.URL.Path)
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	a := &Adapter{id: "gh", owner: "o", repo: "r", client: newClient(srv.URL, "")}

	got, err := a.PollCIStatus(context.Background(), "1958")
	if err != nil {
		t.Fatalf("PollCIStatus: %v", err)
	}
	if got.Status != "passed" {
		t.Errorf("status = %q, want passed (from the OPEN PR #1961, not conflict from closed #1960)", got.Status)
	}
}

// When the issue timeline exists and is parsed successfully but contains no
// cross-referenced PRs, PollCIStatus returns "no_pr" (not "pending") so the
// caller can distinguish "no PR yet" from "PR exists but CI is running".
func TestPollCIStatus_NoPRInTimeline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls/1958"):
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, "/issues/1958/timeline"):
			// Timeline has events but none are PR cross-references.
			_, _ = w.Write([]byte(`[{"event":"labeled","label":{"name":"ai-ready"}}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	a := &Adapter{id: "gh", owner: "o", repo: "r", client: newClient(srv.URL, "")}

	got, err := a.PollCIStatus(context.Background(), "1958")
	if err != nil {
		t.Fatalf("PollCIStatus: %v", err)
	}
	if got.Status != "no_pr" {
		t.Errorf("status = %q, want no_pr (timeline has no PR cross-references)", got.Status)
	}
}

// An empty timeline (not a timeline fetch failure) also signals no PR.
func TestPollCIStatus_EmptyTimeline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls/1958"):
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, "/issues/1958/timeline"):
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	a := &Adapter{id: "gh", owner: "o", repo: "r", client: newClient(srv.URL, "")}

	got, err := a.PollCIStatus(context.Background(), "1958")
	if err != nil {
		t.Fatalf("PollCIStatus: %v", err)
	}
	if got.Status != "no_pr" {
		t.Errorf("status = %q, want no_pr (empty timeline)", got.Status)
	}
}

// A timeline fetch failure is a transient error, not a definitive "no PR" — keep pending.
func TestPollCIStatus_TimelineFetchFailureRemainssPending(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls/1958"):
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, "/issues/1958/timeline"):
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	a := &Adapter{id: "gh", owner: "o", repo: "r", client: newClient(srv.URL, "")}

	got, err := a.PollCIStatus(context.Background(), "1958")
	if err != nil {
		t.Fatalf("PollCIStatus: %v", err)
	}
	if got.Status != "pending" {
		t.Errorf("status = %q, want pending (timeline fetch failure is transient)", got.Status)
	}
}

// Legacy commit statuses (non-Actions CI) still work and are aggregated with checks.
func TestPollCIStatus_LegacyStatusesGreen(t *testing.T) {
	a := ciServer(t,
		`{"state":"success","total_count":2,"statuses":[
			{"context":"ci/lint","state":"success"},
			{"context":"ci/test","state":"success"}
		]}`,
		`{"check_runs":[]}`)

	got, err := a.PollCIStatus(context.Background(), "1958")
	if err != nil {
		t.Fatalf("PollCIStatus: %v", err)
	}
	if got.Status != "passed" {
		t.Errorf("status = %q, want passed (legacy statuses all green)", got.Status)
	}
}
