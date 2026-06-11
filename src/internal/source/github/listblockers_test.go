package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// blockersStub stands up a GitHub API stub for issue 42's dependencies plus the
// blockers' timelines/PRs used by merged detection.
func blockersStub(t *testing.T, routes map[string]string) *Adapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for suffix, body := range routes {
			if strings.HasSuffix(r.URL.Path, suffix) {
				_, _ = w.Write([]byte(body))
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return &Adapter{id: "gh", owner: "o", repo: "r", client: newClient(srv.URL, "")}
}

func TestListBlockers_ClosedIsDone(t *testing.T) {
	a := blockersStub(t, map[string]string{
		"/issues/42/dependencies/blocked_by": `[
			{"id": 1, "number": 49, "title": "Register PAS purpose", "state": "closed"},
			{"id": 2, "number": 50, "title": "Apigee product", "state": "open"}
		]`,
		"/issues/50/timeline": `[]`, // open blocker, no PR linked
	})

	blockers, err := a.ListBlockers(context.Background(), "42", "")
	if err != nil {
		t.Fatalf("ListBlockers: %v", err)
	}
	if len(blockers) != 2 {
		t.Fatalf("got %d blockers, want 2: %+v", len(blockers), blockers)
	}
	if blockers[0].Number != "#49" || blockers[0].State != "done" {
		t.Errorf("closed blocker = %+v, want state normalized to done", blockers[0])
	}
	if blockers[1].State != "open" || blockers[1].Merged {
		t.Errorf("open blocker without PR = %+v, want open/unmerged", blockers[1])
	}
}

func TestListBlockers_OpenBlockerWithMergedPR(t *testing.T) {
	a := blockersStub(t, map[string]string{
		"/issues/42/dependencies/blocked_by": `[
			{"id": 2, "number": 50, "title": "Apigee product", "state": "open"}
		]`,
		"/issues/50/timeline": `[
			{"event":"cross-referenced","source":{"type":"issue","issue":{"number":51,"pull_request":{"url":"https://api/pulls/51"}}}}
		]`,
		"/pulls/51": `{"number": 51, "merged": true}`,
	})

	blockers, err := a.ListBlockers(context.Background(), "42", "")
	if err != nil {
		t.Fatalf("ListBlockers: %v", err)
	}
	if len(blockers) != 1 || !blockers[0].Merged {
		t.Fatalf("got %+v, want the open blocker marked Merged via its PR", blockers)
	}
}

func TestListBlockers_NoBlockers(t *testing.T) {
	a := blockersStub(t, map[string]string{
		"/issues/42/dependencies/blocked_by": `[]`,
	})

	blockers, err := a.ListBlockers(context.Background(), "42", "")
	if err != nil {
		t.Fatalf("ListBlockers: %v", err)
	}
	if len(blockers) != 0 {
		t.Errorf("got %+v, want none", blockers)
	}
}
