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

// A merged PR resolves an open blocker only when its body closes exactly that
// issue. Anything weaker — a plain "Ref #50", a keyword for another repo's
// issue, or a PR that is still open — leaves the blocker unresolved.
func TestListBlockers_OpenBlockerResolvedByMergedClosingPR(t *testing.T) {
	a := blockersStub(t, map[string]string{
		"/issues/42/dependencies/blocked_by": `[
			{"id": 2, "number": 50, "title": "Apigee product", "state": "open"}
		]`,
		"/issues/50/timeline": `[
			{"event":"cross-referenced","source":{"type":"issue","issue":{"number":51,"pull_request":{"url":"https://api/pulls/51"}}}}
		]`,
		"/pulls/51": `{"number": 51, "merged": true, "body": "Adds the Apigee product.\n\nCloses #50"}`,
	})

	blockers, err := a.ListBlockers(context.Background(), "42", "")
	if err != nil {
		t.Fatalf("ListBlockers: %v", err)
	}
	if len(blockers) != 1 || !blockers[0].Merged {
		t.Fatalf("got %+v, want the open blocker marked Merged via the PR that closes it", blockers)
	}
}

// Regression for #8148: a merged PR from a sibling issue that merely cites the
// blocker does not resolve it. Replays the #8073 case — blockers #8071 and #8072
// stayed open while PR #8090 (from sibling issue #8070) cross-referenced both, so
// `await-blockers` released with {"#8071":"merged","#8072":"merged"} and the
// workflow span the 2h implement → check-ci timeout → implement loop.
func TestListBlockers_MergedPRCitingTheBlockerDoesNotResolveIt(t *testing.T) {
	a := blockersStub(t, map[string]string{
		"/issues/42/dependencies/blocked_by": `[
			{"id": 1, "number": 8071, "title": "Contrato de indicadores do gestor", "state": "open"},
			{"id": 2, "number": 8072, "title": "Tela do gestor de produção", "state": "open"}
		]`,
		"/issues/8071/timeline": `[
			{"event":"cross-referenced","source":{"type":"issue","issue":{"number":8090,"pull_request":{"url":"https://api/pulls/8090"}}}}
		]`,
		"/issues/8072/timeline": `[
			{"event":"cross-referenced","source":{"type":"issue","issue":{"number":8090,"pull_request":{"url":"https://api/pulls/8090"}}}},
			{"event":"cross-referenced","source":{"type":"issue","issue":{"number":8094,"pull_request":{"url":"https://api/pulls/8094"}}}}
		]`,
		"/pulls/8090": `{"number": 8090, "merged": true, "body": "Indicadores da visão produção.\n\nRef #8070\nRef #8071\nRef #8072"}`,
		"/pulls/8094": `{"number": 8094, "merged": false, "body": "Painel do gestor.\n\nRef #8072"}`,
	})

	blockers, err := a.ListBlockers(context.Background(), "42", "")
	if err != nil {
		t.Fatalf("ListBlockers: %v", err)
	}
	if len(blockers) != 2 {
		t.Fatalf("got %d blockers, want 2: %+v", len(blockers), blockers)
	}
	for _, b := range blockers {
		if b.State != "open" || b.Merged {
			t.Errorf("blocker %s = %+v, want open/unmerged: a merged PR that only cites it resolves nothing", b.Number, b)
		}
	}
}

func TestListBlockers_MergedPRClosingAnotherReposIssue(t *testing.T) {
	a := blockersStub(t, map[string]string{
		"/issues/42/dependencies/blocked_by": `[
			{"id": 2, "number": 50, "title": "Apigee product", "state": "open"}
		]`,
		"/issues/50/timeline": `[
			{"event":"cross-referenced","source":{"type":"issue","issue":{"number":51,"pull_request":{"url":"https://api/pulls/51"}}}}
		]`,
		"/pulls/51": `{"number": 51, "merged": true, "body": "Mirror of other/repo#50 landed.\n\nCloses other/repo#50"}`,
	})

	blockers, err := a.ListBlockers(context.Background(), "42", "")
	if err != nil {
		t.Fatalf("ListBlockers: %v", err)
	}
	if len(blockers) != 1 || blockers[0].Merged {
		t.Fatalf("got %+v, want unmerged: the keyword closes another repository's #50", blockers)
	}
}

func TestListBlockers_UnmergedPRClosingTheBlocker(t *testing.T) {
	a := blockersStub(t, map[string]string{
		"/issues/42/dependencies/blocked_by": `[
			{"id": 2, "number": 50, "title": "Apigee product", "state": "open"}
		]`,
		"/issues/50/timeline": `[
			{"event":"cross-referenced","source":{"type":"issue","issue":{"number":51,"pull_request":{"url":"https://api/pulls/51"}}}}
		]`,
		"/pulls/51": `{"number": 51, "merged": false, "body": "Closes #50"}`,
	})

	blockers, err := a.ListBlockers(context.Background(), "42", "")
	if err != nil {
		t.Fatalf("ListBlockers: %v", err)
	}
	if len(blockers) != 1 || blockers[0].Merged {
		t.Fatalf("got %+v, want unmerged: the PR that closes it is still open", blockers)
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
