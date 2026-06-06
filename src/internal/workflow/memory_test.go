package workflow

import (
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/model"
)

func TestMemoryBuilder_FullDocument(t *testing.T) {
	cell := model.SourceItem{
		Title:    "Fix user auth bug",
		Labels:   []string{"backend", "bug"},
		Priority: "high",
		SourceID: "main-plane",
	}
	steps := []MemoryStep{
		{
			StepID:      "plan",
			WriteFields: []string{"complexity", "approach"},
			Structured: map[string]any{
				"complexity": "high",
				"approach":   "Refactor auth middleware to use JWT",
			},
			Summary: "- JWT chosen over sessions\n- Two files to change",
		},
		{
			StepID:  "implement",
			Summary: "- Added JwtMiddleware\n- Updated handler",
		},
	}

	doc := MemoryBuilder{}.Build(cell, steps)

	for _, want := range []string{
		"=== Workflow Memory ===",
		"[Cell]",
		"title: Fix user auth bug",
		"labels: backend, bug",
		"priority: high",
		"source: main-plane",
		"[Step Data]",
		"complexity: high",
		"approach: Refactor auth middleware to use JWT",
		"[Summaries]",
		"plan: |",
		"  - JWT chosen over sessions",
		"implement: |",
		"  - Added JwtMiddleware",
		"======================",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("memory document missing %q:\n%s", want, doc)
		}
	}
}

func TestMemoryBuilder_OnlyDeclaredFieldsWritten(t *testing.T) {
	steps := []MemoryStep{
		{
			StepID:      "plan",
			WriteFields: []string{"complexity"}, // approach NOT declared
			Structured: map[string]any{
				"complexity": "high",
				"approach":   "should not appear",
			},
		},
	}
	doc := MemoryBuilder{}.Build(model.SourceItem{Title: "t"}, steps)
	if !strings.Contains(doc, "complexity: high") {
		t.Error("expected declared field complexity")
	}
	if strings.Contains(doc, "should not appear") {
		t.Errorf("undeclared field leaked into memory:\n%s", doc)
	}
}

func TestMemoryBuilder_LastWriteWins(t *testing.T) {
	steps := []MemoryStep{
		{StepID: "a", WriteFields: []string{"status"}, Structured: map[string]any{"status": "draft"}},
		{StepID: "b", WriteFields: []string{"status"}, Structured: map[string]any{"status": "final"}},
	}
	doc := MemoryBuilder{}.Build(model.SourceItem{Title: "t"}, steps)
	if !strings.Contains(doc, "status: final") {
		t.Errorf("expected last write to win:\n%s", doc)
	}
	if strings.Contains(doc, "status: draft") {
		t.Errorf("stale value present:\n%s", doc)
	}
}

func TestMemoryBuilder_ValueRendering(t *testing.T) {
	steps := []MemoryStep{
		{
			StepID:      "s",
			WriteFields: []string{"count", "ratio", "ok", "files"},
			Structured: map[string]any{
				"count": float64(3),
				"ratio": float64(0.5),
				"ok":    true,
				"files": []any{"a.go", "b.go"},
			},
		},
	}
	doc := MemoryBuilder{}.Build(model.SourceItem{Title: "t"}, steps)
	for _, want := range []string{"count: 3", "ratio: 0.5", "ok: true", `files: ["a.go","b.go"]`} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing rendered value %q:\n%s", want, doc)
		}
	}
}

func TestMemoryBuilder_NoStepData(t *testing.T) {
	doc := MemoryBuilder{}.Build(model.SourceItem{Title: "lonely"}, nil)
	if strings.Contains(doc, "[Step Data]") || strings.Contains(doc, "[Summaries]") {
		t.Errorf("empty sections should be omitted:\n%s", doc)
	}
	if !strings.Contains(doc, "title: lonely") {
		t.Error("cell title should always be present")
	}
}

func TestMemoryBuilder_TruncationDropsOldestSummaries(t *testing.T) {
	big := strings.Repeat("x", 500)
	steps := []MemoryStep{
		{StepID: "oldest", Summary: "OLDEST " + big},
		{StepID: "middle", Summary: "MIDDLE " + big},
		{StepID: "newest", Summary: "NEWEST " + big},
	}
	doc := MemoryBuilder{MaxChars: 800}.Build(model.SourceItem{Title: "t"}, steps)

	if len(doc) > 800 {
		t.Errorf("doc exceeds MaxChars: %d", len(doc))
	}
	// Newest summary should survive; oldest should be dropped first.
	if !strings.Contains(doc, "NEWEST") {
		t.Errorf("newest summary should be retained:\n%s", doc)
	}
	if strings.Contains(doc, "OLDEST") {
		t.Errorf("oldest summary should have been dropped:\n%s", doc)
	}
}

func TestMemoryBuilder_CellNeverTruncated(t *testing.T) {
	// Even with a tiny budget, the Cell section must be present.
	doc := MemoryBuilder{MaxChars: 10}.Build(model.SourceItem{Title: "important"}, []MemoryStep{
		{StepID: "s", Summary: strings.Repeat("y", 1000)},
	})
	if !strings.Contains(doc, "=== Workflow Memory ===") {
		t.Errorf("header missing under tight budget:\n%s", doc)
	}
}
