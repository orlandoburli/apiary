package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// The regression from #557: the PR cross-reference is only on page 2 of the
// timeline, so page 1 alone reports "pending" forever. Following Link finds it.
func TestPollCIStatus_FindsPRCrossReferenceOnSecondPage(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls/1958"):
			http.NotFound(w, r) // issue number is not a PR
		case strings.HasSuffix(r.URL.Path, "/issues/1958/timeline"):
			if r.URL.Query().Get("page") == "2" {
				_, _ = w.Write([]byte(`[{"event":"cross-referenced","source":{"type":"issue",
					"issue":{"number":1961,"pull_request":{"url":"https://api/pulls/1961"}}}}]`))
				return
			}
			w.Header().Set("Link", fmt.Sprintf(`<%s/repos/o/r/issues/1958/timeline?per_page=100&page=2>; rel="next"`, srv.URL))
			_, _ = w.Write([]byte(`[{"event":"labeled"}]`))
		case strings.HasSuffix(r.URL.Path, "/pulls/1961"):
			_, _ = w.Write([]byte(`{"number":1961,"state":"open","html_url":"https://gh/pr/1961","head":{"sha":"abc","ref":"agent/task-1958"}}`))
		case strings.HasSuffix(r.URL.Path, "/commits/abc/status"):
			_, _ = w.Write([]byte(`{"state":"success","total_count":1,"statuses":[{"state":"success"}]}`))
		case strings.HasSuffix(r.URL.Path, "/commits/abc/check-runs"):
			_, _ = w.Write([]byte(`{"check_runs":[]}`))
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
		t.Errorf("status = %q, want passed (the PR was only on page 2)", got.Status)
	}
	if got.URL != "https://gh/pr/1961" {
		t.Errorf("url = %q, want the page-2 PR", got.URL)
	}
}

// A timeline fetch failure is an error, not a silent "pending" (#557).
func TestPollCIStatus_TimelineErrorIsNotSilentPending(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls/1958"):
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, "/issues/1958/timeline"):
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{id: "gh", owner: "o", repo: "r", client: newClient(srv.URL, "")}
	got, err := a.PollCIStatus(context.Background(), "1958")
	if err == nil {
		t.Fatal("PollCIStatus returned no error for a failed timeline fetch")
	}
	if got.Status != "pending" {
		t.Errorf("status = %q, want pending on error", got.Status)
	}
}

// A timeline that does not decode is also an error, not "pending".
func TestPollCIStatus_TimelineDecodeErrorIsNotSilentPending(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls/1958"):
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, "/issues/1958/timeline"):
			_, _ = w.Write([]byte(`{"event":`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{id: "gh", owner: "o", repo: "r", client: newClient(srv.URL, "")}
	got, err := a.PollCIStatus(context.Background(), "1958")
	if err == nil {
		t.Fatal("PollCIStatus returned no error for an undecodable timeline")
	}
	if !strings.Contains(err.Error(), "decoding") {
		t.Errorf("error = %v, want a decode error", err)
	}
	if got.Status != "pending" {
		t.Errorf("status = %q, want pending on error", got.Status)
	}
}

// A readable timeline with no PR reference is the only case allowed to stay
// silently pending.
func TestPollCIStatus_NoPRStillPendingWithoutError(t *testing.T) {
	var timelineHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls/1958"):
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, "/issues/1958/timeline"):
			atomic.AddInt32(&timelineHits, 1)
			_, _ = w.Write([]byte(`[{"event":"labeled"}]`))
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
		t.Errorf("status = %q, want pending", got.Status)
	}
	if atomic.LoadInt32(&timelineHits) != 1 {
		t.Errorf("timeline fetched %d times, want 1 (no next link)", timelineHits)
	}
}
