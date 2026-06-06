package plane

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
)

// ── formatComment ─────────────────────────────────────────────────────────────

func TestFormatComment_Success(t *testing.T) {
	result := model.RunResult{
		WorkerID: "backend-dev",
		Success:  true,
		Output:   "refactored login handler",
		Duration: 90 * time.Second,
	}
	html := formatComment(result)

	mustContain(t, html, "✓")
	mustContain(t, html, "backend-dev")
	mustContain(t, html, "refactored login handler")
	mustNotContain(t, html, "✗")
}

func TestFormatComment_Failure(t *testing.T) {
	result := model.RunResult{
		WorkerID: "backend-dev",
		Success:  false,
		Error:    fmt.Errorf("max turns reached"),
		Duration: 30 * time.Second,
	}
	html := formatComment(result)

	mustContain(t, html, "✗")
	mustContain(t, html, "max turns reached")
	mustNotContain(t, html, "✓")
}

func TestFormatComment_EscapesOutput(t *testing.T) {
	result := model.RunResult{
		WorkerID: "w",
		Success:  true,
		Output:   "<script>alert('xss')</script>",
	}
	html := formatComment(result)

	mustNotContain(t, html, "<script>")
	mustContain(t, html, "&lt;script&gt;")
}

// ── matchesFilters ────────────────────────────────────────────────────────────

func TestMatchesFilters_NoFilters(t *testing.T) {
	a := &Adapter{
		stateIDToName: map[string]string{"s1": "Backlog"},
		labelIDToName: map[string]string{},
	}
	if !a.matchesFilters(workItem{State: "s1"}) {
		t.Error("expected match when no filters configured")
	}
}

func TestMatchesFilters_StateMatch(t *testing.T) {
	a := &Adapter{
		filterStates:  []string{"backlog", "todo"},
		stateIDToName: map[string]string{"s1": "Backlog"},
		labelIDToName: map[string]string{},
	}
	if !a.matchesFilters(workItem{State: "s1"}) {
		t.Error("expected state 'Backlog' to match filter 'backlog'")
	}
}

func TestMatchesFilters_StateNoMatch(t *testing.T) {
	a := &Adapter{
		filterStates:  []string{"todo"},
		stateIDToName: map[string]string{"s1": "Backlog"},
		labelIDToName: map[string]string{},
	}
	if a.matchesFilters(workItem{State: "s1"}) {
		t.Error("expected 'Backlog' not to match filter 'todo'")
	}
}

func TestMatchesFilters_AllLabelsRequired(t *testing.T) {
	a := &Adapter{
		filterLabels:  []string{"ai-ready", "backend"},
		stateIDToName: map[string]string{},
		labelIDToName: map[string]string{
			"l1": "ai-ready",
			"l2": "backend",
			"l3": "urgent",
		},
	}

	if !a.matchesFilters(workItem{Labels: []string{"l1", "l2", "l3"}}) {
		t.Error("expected match when all required labels present")
	}
	if a.matchesFilters(workItem{Labels: []string{"l1"}}) {
		t.Error("expected no match when a required label is absent")
	}
}

// ── toSourceItem ────────────────────────────────────────────────────────────────────

func TestToCell_MapsFields(t *testing.T) {
	a := &Adapter{
		workspace:     "my-ws",
		project:       "my-proj",
		stateIDToName: map[string]string{"s1": "In Progress"},
		labelIDToName: map[string]string{"l1": "backend", "l2": "bug"},
	}

	item := workItem{
		ID:                  "item-uuid",
		Name:                "Fix login",
		SequenceID:          42,
		DescriptionStripped: "Login breaks on Safari",
		Priority:            "high",
		State:               "s1",
		Labels:              []string{"l1", "l2"},
		CreatedAt:           "2025-01-01T10:00:00Z",
		UpdatedAt:           "2025-06-01T12:00:00Z",
	}

	cell := a.toSourceItem(item)

	if cell.ID != "item-uuid" {
		t.Errorf("ID = %q, want %q", cell.ID, "item-uuid")
	}
	if cell.Title != "Fix login" {
		t.Errorf("Title = %q, want %q", cell.Title, "Fix login")
	}
	if cell.State != "In Progress" {
		t.Errorf("State = %q, want %q", cell.State, "In Progress")
	}
	if cell.Priority != "high" {
		t.Errorf("Priority = %q, want %q", cell.Priority, "high")
	}
	if len(cell.Labels) != 2 {
		t.Errorf("Labels len = %d, want 2", len(cell.Labels))
	}
	if !strings.Contains(cell.URL, "item-uuid") {
		t.Errorf("URL %q should contain issue uuid", cell.URL)
	}
	if cell.CreatedAt.IsZero() {
		t.Error("CreatedAt should not be zero")
	}
}

func TestToCell_UnknownLabelSkipped(t *testing.T) {
	a := &Adapter{
		workspace:     "ws",
		project:       "proj",
		stateIDToName: map[string]string{},
		labelIDToName: map[string]string{"l1": "known"},
	}
	cell := a.toSourceItem(workItem{Labels: []string{"l1", "unknown-id"}})
	if len(cell.Labels) != 1 || cell.Labels[0] != "known" {
		t.Errorf("expected only known label, got %v", cell.Labels)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

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
