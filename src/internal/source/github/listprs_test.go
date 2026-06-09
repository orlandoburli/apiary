package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// timelineServer stands up a GitHub API stub that serves the given JSON for the
// issue 1958 timeline and 404s everything else.
func timelineServer(t *testing.T, timelineJSON string) *Adapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/issues/1958/timeline") {
			_, _ = w.Write([]byte(timelineJSON))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return &Adapter{id: "gh", owner: "o", repo: "r", client: newClient(srv.URL, "")}
}

func TestListPullRequests_OrderedDedupedDerivedURL(t *testing.T) {
	// Two distinct PRs cross-referenced, plus a non-PR cross-reference and a
	// duplicate of the first PR. Expect [1961, 1970] in timeline order, deduped,
	// with html_url derived from owner/repo/number.
	a := timelineServer(t, `[
		{"event":"cross-referenced","source":{"type":"issue","issue":{"number":1961,"pull_request":{"url":"https://api/pulls/1961"}}}},
		{"event":"commented","source":{"type":"issue","issue":{"number":9999}}},
		{"event":"cross-referenced","source":{"type":"issue","issue":{"number":5000}}},
		{"event":"cross-referenced","source":{"type":"issue","issue":{"number":1970,"pull_request":{"url":"https://api/pulls/1970"}}}},
		{"event":"cross-referenced","source":{"type":"issue","issue":{"number":1961,"pull_request":{"url":"https://api/pulls/1961"}}}}
	]`)

	prs, err := a.ListPullRequests(context.Background(), "1958")
	if err != nil {
		t.Fatalf("ListPullRequests: %v", err)
	}
	if len(prs) != 2 {
		t.Fatalf("got %d PRs, want 2: %+v", len(prs), prs)
	}
	if prs[0].Number != 1961 || prs[1].Number != 1970 {
		t.Errorf("order = [%d, %d], want [1961, 1970]", prs[0].Number, prs[1].Number)
	}
	if want := "https://github.com/o/r/pull/1961"; prs[0].URL != want {
		t.Errorf("URL = %q, want %q", prs[0].URL, want)
	}
}

func TestListPullRequests_EmptyTimeline(t *testing.T) {
	a := timelineServer(t, `[]`)
	prs, err := a.ListPullRequests(context.Background(), "1958")
	if err != nil {
		t.Fatalf("ListPullRequests: %v", err)
	}
	if prs != nil {
		t.Errorf("got %+v, want nil for empty timeline", prs)
	}
}

func TestListPullRequests_HTTPError(t *testing.T) {
	// No timeline handler registered → 404 → error surfaced (not silently empty),
	// so the daemon can preserve last-good persisted data.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	a := &Adapter{id: "gh", owner: "o", repo: "r", client: newClient(srv.URL, "")}

	if _, err := a.ListPullRequests(context.Background(), "1958"); err == nil {
		t.Fatal("expected an error when the timeline request fails")
	}
}
