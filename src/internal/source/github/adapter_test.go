package github

import (
	"context"
	"fmt"
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

	cell := a.toCell(item)

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
	cell := a.toCell(item)
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
	cell := model.Cell{ID: "42", Labels: []string{"agent:engineer", "in-progress", "bug"}}
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
	cell := model.Cell{ID: "42", Labels: []string{"in-progress"}}
	if err := a.RemoveLabels(context.Background(), cell, []string{"in-progress"}); err != nil {
		t.Errorf("expected 404 to be ignored, got %v", err)
	}
}
