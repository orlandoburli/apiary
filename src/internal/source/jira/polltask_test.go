package jira

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const issueBody = `{
	"id": "10001", "key": "ERP-1",
	"fields": {
		"summary": "Add login",
		"description": null,
		"status": {"name": "In Review", "statusCategory": {"key": "indeterminate"}},
		"labels": ["apiary"],
		"created": "2026-06-01T10:00:00.000-0300",
		"updated": "2026-06-09T15:30:00.000-0300"
	}
}`

func adfComment(id int, text string) string {
	return fmt.Sprintf(`{
		"id": "%d",
		"body": {"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"%s"}]}]},
		"created": "2026-06-0%dT10:00:00.000-0300"
	}`, id, text, id)
}

func TestPollTask_FetchesIssueAndPaginatedComments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/issue/10001/comment"):
			if r.URL.Query().Get("startAt") == "0" {
				_, _ = fmt.Fprintf(w, `{"comments": [%s, %s], "total": 3}`,
					adfComment(1, "needs work"), adfComment(2, "fixed"))
			} else {
				_, _ = fmt.Fprintf(w, `{"comments": [%s], "total": 3}`,
					adfComment(3, "looks good, approve"))
			}
		case strings.HasSuffix(r.URL.Path, "/issue/10001"):
			_, _ = w.Write([]byte(issueBody))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	cell, err := a.PollTask(context.Background(), "10001")
	if err != nil {
		t.Fatalf("PollTask: %v", err)
	}
	if cell.ID != "10001" || cell.Number != "ERP-1" || cell.State != "In Review" {
		t.Errorf("issue fields wrong: %+v", cell)
	}
	if len(cell.Comments) != 3 {
		t.Fatalf("expected 3 comments across pages, got %d", len(cell.Comments))
	}
	if cell.Comments[2].Body != "looks good, approve" {
		t.Errorf("ADF comment body not flattened: %q", cell.Comments[2].Body)
	}
	if cell.Comments[0].CreatedAt.IsZero() {
		t.Error("comment created not parsed")
	}
}

func TestPollTask_CommentFailureIsNonFatal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/comment"):
			http.Error(w, "boom", http.StatusInternalServerError)
		case strings.HasSuffix(r.URL.Path, "/issue/10001"):
			_, _ = w.Write([]byte(issueBody))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	cell, err := a.PollTask(context.Background(), "10001")
	if err != nil {
		t.Fatalf("comment failure must not fail PollTask: %v", err)
	}
	if len(cell.Comments) != 0 {
		t.Errorf("expected no comments, got %d", len(cell.Comments))
	}
}

func TestPollTask_IssueErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	if _, err := a.PollTask(context.Background(), "999"); err == nil {
		t.Error("expected error when issue fetch fails")
	}
}
