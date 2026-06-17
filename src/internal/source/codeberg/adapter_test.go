package codeberg

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/source"
)

func TestToSourceItem_MapsFields(t *testing.T) {
	a := &Adapter{id: "cb-test"}
	item := issue{
		Number:    42,
		Title:     "Fix login bug",
		Body:      "Users cannot log in",
		State:     "open",
		Labels:    []label{{ID: 1, Name: "Bug"}, {ID: 2, Name: "backend"}},
		HTMLURL:   "https://codeberg.org/o/r/issues/42",
		CreatedAt: "2025-01-01T10:00:00Z",
		UpdatedAt: "2025-06-15T12:00:00Z",
	}
	cell := a.toSourceItem(item)

	if cell.ID != "42" || cell.Number != "#42" {
		t.Errorf("ID/Number = %q/%q, want 42/#42", cell.ID, cell.Number)
	}
	if cell.SourceID != "cb-test" {
		t.Errorf("SourceID = %q", cell.SourceID)
	}
	if cell.Type != "issue" {
		t.Errorf("Type = %q, want issue", cell.Type)
	}
	if len(cell.Labels) != 2 || cell.Labels[0] != "bug" || cell.Labels[1] != "backend" {
		t.Errorf("Labels = %v, want lowercased [bug backend]", cell.Labels)
	}
	if cell.CreatedAt.IsZero() || cell.UpdatedAt.IsZero() {
		t.Error("timestamps should not be zero")
	}
}

func TestMatchesFilters(t *testing.T) {
	if !(&Adapter{}).matchesFilters(issue{Number: 1, State: "open"}) {
		t.Error("no filters should match")
	}
	if (&Adapter{filterStates: []string{"closed"}}).matchesFilters(issue{State: "open"}) {
		t.Error("open should not match state filter [closed]")
	}
	a := &Adapter{filterLabels: []string{"bug", "backend"}}
	if !a.matchesFilters(issue{Labels: []label{{Name: "bug"}, {Name: "backend"}, {Name: "x"}}}) {
		t.Error("all required labels present should match")
	}
	if a.matchesFilters(issue{Labels: []label{{Name: "bug"}}}) {
		t.Error("missing a required label should not match")
	}
}

func TestDeriveWebBaseURL(t *testing.T) {
	cases := map[string]string{
		"":                                   "https://codeberg.org",
		defaultBaseURL:                       "https://codeberg.org",
		"https://git.example.org/api/v1":     "https://git.example.org",
		"https://forge.internal:3000/api/v1": "https://forge.internal:3000",
	}
	for in, want := range cases {
		if got := deriveWebBaseURL(in); got != want {
			t.Errorf("deriveWebBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeStatus(t *testing.T) {
	cases := map[string]string{
		"success": "passed", "warning": "passed",
		"failure": "failed", "error": "failed",
		"pending": "pending", "skipped": "skipped", "weird": "unknown",
	}
	for in, want := range cases {
		if got := normalizeStatus(in); got != want {
			t.Errorf("normalizeStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatComment(t *testing.T) {
	ok := formatComment(model.RunResult{WorkerID: "eng", Success: true, Output: "done it", Duration: 90 * time.Second})
	mustContain(t, ok, "✓")
	mustContain(t, ok, "eng")
	mustContain(t, ok, "done it")
	mustNotContain(t, ok, "✗")

	fail := formatComment(model.RunResult{WorkerID: "eng", Success: false, Error: fmt.Errorf("boom"), Duration: time.Second})
	mustContain(t, fail, "✗")
	mustContain(t, fail, "boom")
}

// TestPoll_SkipsPullRequests verifies a PR returned by the /issues endpoint is
// not ingested as a task — only plain issues become SourceItems.
func TestPoll_SkipsPullRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("type") != "issues" {
			t.Errorf("expected type=issues query, got %q", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`[
			{"number": 1, "title": "Real issue", "state": "open"},
			{"number": 2, "title": "A PR", "state": "open", "pull_request": {"merged": false}}
		]`))
	}))
	defer srv.Close()

	a := &Adapter{id: "cb", owner: "o", repo: "r", client: newClient(srv.URL, "")}
	items, err := a.Poll(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(items) != 1 || items[0].ID != "1" {
		t.Fatalf("got %+v, want only issue #1", items)
	}
}

// TestAddLabels_ResolvesNamesToIDs verifies the adapter lists labels, creates a
// missing one (with a color), and POSTs label ids — not names — to the issue.
func TestAddLabels_ResolvesNamesToIDs(t *testing.T) {
	var postedLabels, createBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/labels"):
			_, _ = w.Write([]byte(`[{"id": 10, "name": "in-progress", "color": "ededed"}]`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/repos/o/r/labels"):
			b, _ := io.ReadAll(r.Body)
			createBody = string(b)
			_, _ = w.Write([]byte(`{"id": 20, "name": "agent:engineer", "color": "ededed"}`))
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/issues/42/labels"):
			b, _ := io.ReadAll(r.Body)
			postedLabels = string(b)
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := &Adapter{id: "cb", owner: "o", repo: "r", client: newClient(srv.URL, ""), labelByName: map[string]int64{}}
	cell := model.SourceItem{ID: "42"}
	if err := a.AddLabels(context.Background(), cell, []string{"in-progress", "agent:engineer"}); err != nil {
		t.Fatalf("AddLabels: %v", err)
	}
	// in-progress (10) resolved from the listing, agent:engineer (20) created.
	if !strings.Contains(postedLabels, "10") || !strings.Contains(postedLabels, "20") {
		t.Errorf("posted labels = %q, want ids 10 and 20", postedLabels)
	}
	if !strings.Contains(createBody, `"agent:engineer"`) || !strings.Contains(createBody, `"color"`) {
		t.Errorf("create body = %q, want name + required color", createBody)
	}
}

// TestRemoveLabels_DeletesByID verifies labels are resolved to ids and removed
// one DELETE per id, and that a name absent from the repo is silently skipped.
func TestRemoveLabels_DeletesByID(t *testing.T) {
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/labels"):
			_, _ = w.Write([]byte(`[{"id": 10, "name": "in-progress"}, {"id": 11, "name": "agent:engineer"}]`))
		case r.Method == http.MethodDelete:
			i := strings.LastIndex(r.URL.Path, "/labels/")
			deleted = append(deleted, r.URL.Path[i+len("/labels/"):])
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := &Adapter{id: "cb", owner: "o", repo: "r", client: newClient(srv.URL, ""), labelByName: map[string]int64{}}
	cell := model.SourceItem{ID: "42"}
	// "never-existed" is not on the repo and must be skipped, not error.
	if err := a.RemoveLabels(context.Background(), cell, []string{"in-progress", "never-existed", "agent:engineer"}); err != nil {
		t.Fatalf("RemoveLabels: %v", err)
	}
	if len(deleted) != 2 || deleted[0] != "10" || deleted[1] != "11" {
		t.Errorf("deleted ids = %v, want [10 11]", deleted)
	}
}

// TestPollCIStatus_Conflict verifies an unmerged, non-mergeable PR surfaces as a
// conflict without fetching commit status.
func TestPollCIStatus_Conflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/timeline"):
			_, _ = w.Write([]byte(`[{"type":"comment_ref","ref_issue":{"number":7,"pull_request":{"merged":false}}}]`))
		case strings.Contains(r.URL.Path, "/pulls/7"):
			_, _ = w.Write([]byte(`{"number":7,"mergeable":false,"merged":false,"html_url":"https://codeberg.org/o/r/pulls/7","head":{"sha":"abc"}}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := &Adapter{id: "cb", owner: "o", repo: "r", webBaseURL: "https://codeberg.org", client: newClient(srv.URL, "")}
	st, err := a.PollCIStatus(context.Background(), "5")
	if err != nil {
		t.Fatalf("PollCIStatus: %v", err)
	}
	if st.Status != "conflict" {
		t.Errorf("status = %q, want conflict", st.Status)
	}
}

// TestPollCIStatus_AggregatesStatuses verifies commit statuses aggregate to an
// overall verdict and that a failing context wins.
func TestPollCIStatus_AggregatesStatuses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/timeline"):
			_, _ = w.Write([]byte(`[{"ref_issue":{"number":7,"pull_request":{"merged":false}}}]`))
		case strings.Contains(r.URL.Path, "/pulls/7"):
			_, _ = w.Write([]byte(`{"number":7,"mergeable":true,"merged":false,"head":{"sha":"abc"}}`))
		case strings.Contains(r.URL.Path, "/commits/abc/status"):
			_, _ = w.Write([]byte(`{"state":"failure","total_count":2,"statuses":[{"context":"lint","status":"success"},{"context":"test","status":"failure"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := &Adapter{id: "cb", owner: "o", repo: "r", webBaseURL: "https://codeberg.org", client: newClient(srv.URL, "")}
	st, err := a.PollCIStatus(context.Background(), "5")
	if err != nil {
		t.Fatalf("PollCIStatus: %v", err)
	}
	if st.Status != "failed" {
		t.Errorf("status = %q, want failed", st.Status)
	}
	if len(st.Checks) != 2 {
		t.Errorf("checks = %d, want 2", len(st.Checks))
	}
}

// TestListBlockers_NormalizesState verifies closed blockers become "done" and a
// blocker that is itself a merged PR reports Merged.
func TestListBlockers_NormalizesState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"number": 3, "title": "closed dep", "state": "closed"},
			{"number": 4, "title": "open PR dep", "state": "open", "pull_request": {"merged": true}}
		]`))
	}))
	defer srv.Close()

	a := &Adapter{id: "cb", owner: "o", repo: "r", client: newClient(srv.URL, "")}
	blockers, err := a.ListBlockers(context.Background(), "9", "")
	if err != nil {
		t.Fatalf("ListBlockers: %v", err)
	}
	if len(blockers) != 2 {
		t.Fatalf("got %d blockers, want 2", len(blockers))
	}
	if blockers[0].State != "done" {
		t.Errorf("closed blocker state = %q, want done", blockers[0].State)
	}
	if !blockers[1].Merged {
		t.Errorf("merged-PR blocker should report Merged=true")
	}
}

func TestImplementsCapabilities(t *testing.T) {
	var a any = &Adapter{}
	if _, ok := a.(source.SubIssueCreator); ok {
		t.Error("Codeberg must NOT implement SubIssueCreator (Forgejo has no sub-issue API)")
	}
	if _, ok := a.(source.CIStatusPoller); !ok {
		t.Error("Codeberg should implement CIStatusPoller")
	}
	if _, ok := a.(source.BlockerLister); !ok {
		t.Error("Codeberg should implement BlockerLister")
	}
}

func mustContain(t *testing.T, s, sub string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Errorf("expected %q to contain %q", s, sub)
	}
}

func mustNotContain(t *testing.T, s, sub string) {
	t.Helper()
	if strings.Contains(s, sub) {
		t.Errorf("expected %q NOT to contain %q", s, sub)
	}
}
