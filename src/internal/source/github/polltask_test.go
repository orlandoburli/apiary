package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPollTask_FetchesIssueAndComments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/issues/42/comments"):
			_, _ = w.Write([]byte(`[
				{"id": 1, "body": "needs work", "created_at": "2025-01-01T10:00:00Z"},
				{"id": 2, "body": "looks good, approve", "created_at": "2025-01-02T10:00:00Z"}
			]`))
		case strings.HasSuffix(r.URL.Path, "/issues/42"):
			_, _ = w.Write([]byte(`{
				"number": 42, "title": "Add auth", "state": "open",
				"labels": [{"id": 1, "name": "feature"}],
				"html_url": "https://github.com/o/r/issues/42",
				"created_at": "2025-01-01T09:00:00Z", "updated_at": "2025-01-02T11:00:00Z"
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := &Adapter{id: "gh", owner: "o", repo: "r", client: newClient(srv.URL, "")}

	cell, err := a.PollTask(context.Background(), "42")
	if err != nil {
		t.Fatalf("PollTask: %v", err)
	}
	if cell.ID != "42" || cell.Title != "Add auth" || cell.State != "open" {
		t.Errorf("issue fields wrong: %+v", cell)
	}
	if len(cell.Comments) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(cell.Comments))
	}
	if cell.Comments[1].Body != "looks good, approve" {
		t.Errorf("comment body wrong: %q", cell.Comments[1].Body)
	}
	if cell.Comments[0].CreatedAt.IsZero() {
		t.Error("comment created_at not parsed")
	}
}

func TestPollTask_IssueErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	a := &Adapter{id: "gh", owner: "o", repo: "r", client: newClient(srv.URL, "")}
	if _, err := a.PollTask(context.Background(), "999"); err == nil {
		t.Error("expected error when issue fetch fails")
	}
}
