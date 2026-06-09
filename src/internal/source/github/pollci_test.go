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
