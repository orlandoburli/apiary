package github

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
)

func TestToCell_MapsFields(t *testing.T) {
	a := &Adapter{id: "gh-test"}

	item := issue{
		Number:    42,
		Title:     "Fix login bug",
		Body:      "Users cannot log in on Safari",
		State:     "open",
		Labels:    []label{{ID: 1, Name: "bug"}, {ID: 2, Name: "backend"}},
		HTMLURL:   "https://github.com/owner/repo/issues/42",
		CreatedAt: "2025-01-01T10:00:00Z",
		UpdatedAt: "2025-06-15T12:00:00Z",
	}

	cell := a.toSourceItem(item)

	if cell.ID != "42" {
		t.Errorf("ID = %q, want %q", cell.ID, "42")
	}
	if cell.SourceID != "gh-test" {
		t.Errorf("SourceID = %q, want %q", cell.SourceID, "gh-test")
	}
	if cell.Number != "#42" {
		t.Errorf("Number = %q, want %q", cell.Number, "#42")
	}
	if cell.Title != "Fix login bug" {
		t.Errorf("Title = %q, want %q", cell.Title, "Fix login bug")
	}
	if cell.Description != "Users cannot log in on Safari" {
		t.Errorf("Description = %q, want %q", cell.Description, "Users cannot log in on Safari")
	}
	if cell.State != "open" {
		t.Errorf("State = %q, want %q", cell.State, "open")
	}
	if len(cell.Labels) != 2 {
		t.Fatalf("Labels len = %d, want 2", len(cell.Labels))
	}
	if cell.Labels[0] != "bug" || cell.Labels[1] != "backend" {
		t.Errorf("Labels = %v, want [bug backend]", cell.Labels)
	}
	if cell.URL != "https://github.com/owner/repo/issues/42" {
		t.Errorf("URL = %q, want %q", cell.URL, "https://github.com/owner/repo/issues/42")
	}
	if cell.CreatedAt.IsZero() {
		t.Error("CreatedAt should not be zero")
	}
	if cell.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should not be zero")
	}
}

func TestToCell_ClosedIssue(t *testing.T) {
	a := &Adapter{}
	item := issue{
		Number: 7,
		Title:  "Done task",
		State:  "closed",
	}
	cell := a.toSourceItem(item)
	if cell.State != "closed" {
		t.Errorf("State = %q, want %q", cell.State, "closed")
	}
}

func TestMatchesFilters_NoFilters(t *testing.T) {
	a := &Adapter{}
	if !a.matchesFilters(issue{Number: 1, State: "open"}) {
		t.Error("expected match when no filters configured")
	}
}

func TestMatchesFilters_StateMatch(t *testing.T) {
	a := &Adapter{
		filterStates: []string{"open"},
	}
	if !a.matchesFilters(issue{Number: 1, State: "open"}) {
		t.Error("expected 'open' to match filter 'open'")
	}
}

func TestMatchesFilters_StateNoMatch(t *testing.T) {
	a := &Adapter{
		filterStates: []string{"closed"},
	}
	if a.matchesFilters(issue{Number: 1, State: "open"}) {
		t.Error("expected 'open' NOT to match filter 'closed'")
	}
}

func TestMatchesFilters_LabelsRequired(t *testing.T) {
	a := &Adapter{
		filterLabels: []string{"bug", "backend"},
	}

	if !a.matchesFilters(issue{
		Number: 1,
		Labels: []label{{Name: "bug"}, {Name: "backend"}, {Name: "urgent"}},
	}) {
		t.Error("expected match when all required labels present")
	}

	if a.matchesFilters(issue{
		Number: 2,
		Labels: []label{{Name: "bug"}},
	}) {
		t.Error("expected no match when a required label is absent")
	}
}

func TestFormatComment_Success(t *testing.T) {
	result := model.RunResult{
		WorkerID: "backend-dev",
		Success:  true,
		Output:   "refactored login handler",
		Duration: 90 * time.Second,
	}
	body := formatComment(result)

	mustContain(t, body, "✓")
	mustContain(t, body, "backend-dev")
	mustContain(t, body, "refactored login handler")
	mustNotContain(t, body, "✗")
	mustNotContain(t, body, "Error")
}

func TestFormatComment_Failure(t *testing.T) {
	result := model.RunResult{
		WorkerID: "backend-dev",
		Success:  false,
		Error:    fmt.Errorf("max turns reached"),
		Duration: 30 * time.Second,
	}
	body := formatComment(result)

	mustContain(t, body, "✗")
	mustContain(t, body, "max turns reached")
	mustNotContain(t, body, "✓")
}

func TestFormatComment_NoOutput(t *testing.T) {
	result := model.RunResult{
		WorkerID: "w",
		Success:  true,
		Duration: 5 * time.Second,
	}
	body := formatComment(result)
	mustContain(t, body, "Apiary run complete")
	mustNotContain(t, body, "```")
}

func TestContainsAny(t *testing.T) {
	if !containsAny([]string{"a", "b", "c"}, "b") {
		t.Error("expected true for 'b' in [a b c]")
	}
	if containsAny([]string{"a", "b", "c"}, "d") {
		t.Error("expected false for 'd' in [a b c]")
	}
	if containsAny(nil, "x") {
		t.Error("expected false for nil slice")
	}
}

func mustContain(t *testing.T, s, sub string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Errorf("expected output to contain %q\ngot: %s", sub, s)
	}
}

func mustNotContain(t *testing.T, s, sub string) {
	t.Helper()
	if strings.Contains(s, sub) {
		t.Errorf("expected output NOT to contain %q\ngot: %s", sub, s)
	}
}

func TestRemoveLabels_DeletesEachLabel(t *testing.T) {
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			idx := strings.LastIndex(r.URL.Path, "/labels/")
			deleted = append(deleted, r.URL.Path[idx+len("/labels/"):])
			_, _ = w.Write([]byte(`[]`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	a := &Adapter{id: "gh", owner: "o", repo: "r", client: newClient(srv.URL, "")}
	cell := model.SourceItem{ID: "42", Labels: []string{"agent:engineer", "in-progress", "bug"}}
	if err := a.RemoveLabels(context.Background(), cell, []string{"agent:engineer", "in-progress"}); err != nil {
		t.Fatalf("RemoveLabels: %v", err)
	}
	if len(deleted) != 2 || deleted[0] != "agent:engineer" || deleted[1] != "in-progress" {
		t.Errorf("deleted = %v, want [agent:engineer in-progress]", deleted)
	}
}

func TestRemoveLabels_Ignores404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Label does not exist"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	a := &Adapter{id: "gh", owner: "o", repo: "r", client: newClient(srv.URL, "")}
	cell := model.SourceItem{ID: "42", Labels: []string{"in-progress"}}
	if err := a.RemoveLabels(context.Background(), cell, []string{"in-progress"}); err != nil {
		t.Errorf("expected 404 to be ignored, got %v", err)
	}
}

// TestCreateSubIssue_CreatesAndLinks verifies that CreateSubIssue POSTs a new
// issue (title, body, labels) and then links it under the parent via the
// sub_issues endpoint using the created issue's REST id, returning the new item.
func TestCreateSubIssue_CreatesAndLinks(t *testing.T) {
	var createdBody, linkBody string
	var ensuredLabels []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/labels"):
			b, _ := io.ReadAll(r.Body)
			ensuredLabels = append(ensuredLabels, string(b))
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/sub_issues"):
			b, _ := io.ReadAll(r.Body)
			linkBody = string(b)
			if !strings.HasSuffix(r.URL.Path, "/issues/42/sub_issues") {
				t.Errorf("link path = %q, want under parent 42", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues"):
			b, _ := io.ReadAll(r.Body)
			createdBody = string(b)
			_, _ = w.Write([]byte(`{"id": 555, "number": 101, "html_url": "https://github.com/o/r/issues/101", "title": "Backend", "state": "open"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := &Adapter{id: "github", owner: "o", repo: "r", client: newClient(srv.URL, "")}
	parent := model.SourceItem{ID: "42", Number: "#42", SourceID: "github"}
	child := model.SourceItem{Title: "Backend", Description: "spec b", Labels: []string{"agent:backend"}}

	created, err := a.CreateSubIssue(context.Background(), parent, child)
	if err != nil {
		t.Fatalf("CreateSubIssue: %v", err)
	}
	if created.ID != "101" || created.Number != "#101" {
		t.Errorf("created item = %+v, want number 101", created)
	}
	if created.URL != "https://github.com/o/r/issues/101" {
		t.Errorf("created URL = %q", created.URL)
	}
	if !strings.Contains(createdBody, `"title":"Backend"`) || !strings.Contains(createdBody, `"agent:backend"`) {
		t.Errorf("create body missing title/labels: %s", createdBody)
	}
	if !strings.Contains(linkBody, `"sub_issue_id":555`) {
		t.Errorf("link body = %q, want sub_issue_id 555 (the REST id, not number)", linkBody)
	}
	if len(ensuredLabels) == 0 {
		t.Error("expected the child's labels to be ensured before creation")
	}
}

// TestCreateSubIssue_LinkFailureNonFatal verifies that when the issue is created
// but linking it under the parent fails, CreateSubIssue still returns the created
// item (so the caller persists the binding and never re-creates a duplicate).
func TestCreateSubIssue_LinkFailureNonFatal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/sub_issues"):
			http.Error(w, `{"message":"sub-issues unavailable"}`, http.StatusForbidden)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues"):
			_, _ = w.Write([]byte(`{"id": 7, "number": 102, "html_url": "https://github.com/o/r/issues/102"}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	a := &Adapter{id: "github", owner: "o", repo: "r", client: newClient(srv.URL, "")}
	created, err := a.CreateSubIssue(context.Background(),
		model.SourceItem{ID: "42", Number: "#42"},
		model.SourceItem{Title: "Backend"})
	if err != nil {
		t.Fatalf("link failure must be non-fatal, got error: %v", err)
	}
	if created.Number != "#102" {
		t.Errorf("created item = %+v, want number 102 despite link failure", created)
	}
}

// TestPoll_SkipsPullRequests verifies that pull requests returned by GitHub's
// /issues endpoint (every PR is also an issue in the API) are not ingested as
// tasks — only plain issues become SourceItems.
func TestPoll_SkipsPullRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"number": 1, "title": "A real issue", "state": "open"},
			{"number": 2, "title": "A pull request", "state": "open", "pull_request": {}}
		]`))
	}))
	defer srv.Close()

	a := &Adapter{id: "gh", owner: "o", repo: "r", client: newClient(srv.URL, "")}
	items, err := a.Poll(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1 (PR should be skipped): %+v", len(items), items)
	}
	if items[0].ID != "1" {
		t.Errorf("ingested item ID = %q, want %q (the issue, not the PR)", items[0].ID, "1")
	}
	if items[0].Type != "issue" {
		t.Errorf("Type = %q, want %q", items[0].Type, "issue")
	}
}

// TestAddLabels_AppendsWithoutReplacing verifies that AddLabels uses the
// additive POST /issues/{n}/labels endpoint sending ONLY the new labels —
// never a PATCH replaying the cell's (possibly stale) label snapshot, which
// would revert labels an agent swapped while the run was executing.
func TestAddLabels_AppendsWithoutReplacing(t *testing.T) {
	var issuePatched bool
	var postedBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/labels":
			// ensureLabel: label already exists.
			http.Error(w, `{"message":"already_exists"}`, http.StatusUnprocessableEntity)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues/42/labels":
			b, _ := io.ReadAll(r.Body)
			postedBody = string(b)
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPatch:
			issuePatched = true
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := &Adapter{id: "gh", owner: "o", repo: "r", client: newClient(srv.URL, "")}
	// Stale snapshot: the agent already swapped workflow:spec → workflow:implementation
	// on the live issue; the snapshot must not leak into the request.
	cell := model.SourceItem{ID: "42", Labels: []string{"workflow:spec"}}
	if err := a.AddLabels(context.Background(), cell, []string{"po:done"}); err != nil {
		t.Fatalf("AddLabels: %v", err)
	}
	if issuePatched {
		t.Error("AddLabels must not PATCH the issue (label replace) — additive POST only")
	}
	if postedBody == "" {
		t.Fatal("expected POST to /issues/42/labels")
	}
	if !strings.Contains(postedBody, "po:done") {
		t.Errorf("posted body missing new label: %s", postedBody)
	}
	if strings.Contains(postedBody, "workflow:spec") {
		t.Errorf("posted body must not replay the snapshot labels: %s", postedBody)
	}
}

func TestToCell_MapsAuthorAssociation(t *testing.T) {
	a := &Adapter{id: "gh"}
	for _, assoc := range []string{"OWNER", "MEMBER", "COLLABORATOR", "NONE", ""} {
		item := issue{Number: 1, AuthorAssociation: assoc}
		cell := a.toSourceItem(item)
		if cell.AuthorAssociation != assoc {
			t.Errorf("assoc %q: toSourceItem set AuthorAssociation=%q", assoc, cell.AuthorAssociation)
		}
	}
}
