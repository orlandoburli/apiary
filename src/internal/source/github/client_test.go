package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// Page 1 advertises page 2 through Link rel="next"; the reader must follow it
// instead of stopping at the first page.
func TestGetAllIssuesFollowsNextLink(t *testing.T) {
	var page2Hits int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("per_page"); got != "100" {
			t.Errorf("per_page = %q, want 100", got)
		}
		switch r.URL.Query().Get("page") {
		case "": // first page
			w.Header().Set("Link", fmt.Sprintf(`<%s/repos/o/r/issues?per_page=100&page=2>; rel="next"`, srv.URL))
			_, _ = w.Write([]byte(`[{"number":1,"title":"one"}]`))
		case "2":
			atomic.AddInt32(&page2Hits, 1)
			_, _ = w.Write([]byte(`[{"number":2,"title":"two"}]`))
		default:
			t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
		}
	}))
	t.Cleanup(srv.Close)

	issues, err := newClient(srv.URL, "").getAllIssues(context.Background(),
		"/repos/o/r/issues", url.Values{"state": {"open"}})
	if err != nil {
		t.Fatalf("getAllIssues: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("got %d issues, want 2 (page 2 was not followed)", len(issues))
	}
	if issues[1].Number != 2 {
		t.Errorf("second issue #%d, want #2", issues[1].Number)
	}
	if got := atomic.LoadInt32(&page2Hits); got != 1 {
		t.Errorf("page 2 fetched %d times, want 1", got)
	}
}

// A truncated body must surface as the read failure, not as the JSON decoder's
// "unexpected end of JSON input" (#558).
func TestGetAllIssuesTruncatedBodyReportsReadError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "512") // more than we write
		_, _ = w.Write([]byte(`[{"number":1`))
	}))
	t.Cleanup(srv.Close)

	_, err := newClient(srv.URL, "").getAllIssues(context.Background(),
		"/repos/o/r/issues", nil)
	if err == nil {
		t.Fatal("getAllIssues succeeded on a truncated body")
	}
	if !strings.Contains(err.Error(), "unexpected EOF") {
		t.Errorf("error = %v, want the body-read error (unexpected EOF)", err)
	}
	if strings.Contains(err.Error(), "unexpected end of JSON input") {
		t.Errorf("error = %v, want the read error, not a misleading decode error", err)
	}
}

// do() must also report a body-read failure instead of letting the caller decode
// a partial body.
func TestGetTruncatedBodyReportsReadError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "512")
		_, _ = w.Write([]byte(`{"number":1`))
	}))
	t.Cleanup(srv.Close)

	_, err := newClient(srv.URL, "").get(context.Background(), "/repos/o/r/pulls/1")
	if err == nil || !strings.Contains(err.Error(), "unexpected EOF") {
		t.Errorf("error = %v, want a body-read error", err)
	}
}

// A GET whose first attempt is cut off mid-body is retried and succeeds.
func TestGetRetriesTransientBodyError(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.Header().Set("Content-Length", "512")
			_, _ = w.Write([]byte(`[{"number":1`))
			return
		}
		_, _ = w.Write([]byte(`[{"number":1,"title":"one"}]`))
	}))
	t.Cleanup(srv.Close)

	issues, err := newClient(srv.URL, "").getAllIssues(context.Background(),
		"/repos/o/r/issues", nil)
	if err != nil {
		t.Fatalf("getAllIssues after retry: %v", err)
	}
	if len(issues) != 1 || issues[0].Number != 1 {
		t.Fatalf("got %+v, want the retried page", issues)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Errorf("attempts = %d, want 2 (one retry)", got)
	}
}

// An API error is GitHub's answer, so it must not be replayed.
func TestGetDoesNotRetryAPIError(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	_, err := newClient(srv.URL, "").get(context.Background(), "/repos/o/r/pulls/1")
	if err == nil || !strings.Contains(err.Error(), "status 404") {
		t.Fatalf("error = %v, want a 404", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Errorf("attempts = %d, want 1 (API errors are not retried)", got)
	}
}

// A cancelled context must not be retried, even if the failure looks transient.
func TestGetDoesNotRetryAfterCancel(t *testing.T) {
	var attempts int32
	ctx, cancel := context.WithCancel(context.Background())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		cancel()
		w.Header().Set("Content-Length", "512")
		_, _ = w.Write([]byte(`[{"number":1`))
	}))
	t.Cleanup(srv.Close)

	_, err := newClient(srv.URL, "").getAllIssues(ctx, "/repos/o/r/issues", nil)
	if err == nil {
		t.Fatal("getAllIssues succeeded with a cancelled context")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Errorf("attempts = %d, want 1 (a cancelled context is final)", got)
	}
}
