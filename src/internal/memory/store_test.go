package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.now = func() time.Time { return time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC) }
	return s
}

func TestUpsertGlobal_RoundTrip(t *testing.T) {
	s := testStore(t)
	in := Entry{
		Name:        "ci-duration",
		Description: "CI takes ~12 minutes",
		Content:     "Poll with a 12m budget.\nSecond line.",
		Agent:       "engineer",
		Task:        "01ABC",
		Workflow:    "engineer-workflow",
	}
	if err := s.UpsertGlobal(in); err != nil {
		t.Fatalf("UpsertGlobal: %v", err)
	}
	got, err := s.Read("ci-duration")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Description != in.Description || got.Content != in.Content ||
		got.Agent != in.Agent || got.Task != in.Task || got.Workflow != in.Workflow {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if got.Created.IsZero() || got.Updated.IsZero() {
		t.Fatalf("timestamps not set: %+v", got)
	}

	idx, err := os.ReadFile(filepath.Join(s.Root(), IndexFile))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if !strings.Contains(string(idx), "ci-duration — CI takes ~12 minutes") {
		t.Fatalf("index missing entry: %s", idx)
	}
}

func TestUpsertGlobal_UpdateKeepsCreated(t *testing.T) {
	s := testStore(t)
	if err := s.UpsertGlobal(Entry{Name: "fact", Description: "v1", Content: "one"}); err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return created.Add(48 * time.Hour) }
	if err := s.UpsertGlobal(Entry{Name: "fact", Description: "v2", Content: "two"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Read("fact")
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "two" || got.Description != "v2" {
		t.Fatalf("update not applied: %+v", got)
	}
	if !got.Created.Equal(created) {
		t.Fatalf("created not preserved: %v", got.Created)
	}
	if !got.Updated.After(got.Created) {
		t.Fatalf("updated not bumped: %v", got.Updated)
	}
}

func TestUpsertGlobal_Validation(t *testing.T) {
	s := testStore(t)
	cases := []Entry{
		{Name: "../escape", Description: "d", Content: "c"},
		{Name: "Has Spaces", Description: "d", Content: "c"},
		{Name: "ok-name", Description: "", Content: "c"},
		{Name: "ok-name", Description: "d", Content: ""},
		{Name: "ok-name", Description: "d", Content: strings.Repeat("x", DefaultMaxEntryBytes+1)},
	}
	for i, e := range cases {
		if err := s.UpsertGlobal(e); err == nil {
			t.Fatalf("case %d: expected error for %+v", i, e)
		}
	}
	if _, err := os.Stat(filepath.Join(s.Root(), "global", "ok-name.md")); !os.IsNotExist(err) {
		t.Fatalf("rejected entry was written")
	}
}

func TestAppendTaskNote_DedupAndOrder(t *testing.T) {
	s := testStore(t)
	n := Note{Content: "root cause is in adf.go", Agent: "triage", Workflow: "triage", Step: "analyze"}
	if err := s.AppendTaskNote("01TASK", n); err != nil {
		t.Fatal(err)
	}
	// Identical content again (a retry) — skipped.
	if err := s.AppendTaskNote("01TASK", n); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendTaskNote("01TASK", Note{Content: "fix lives in markdown.go", Agent: "engineer"}); err != nil {
		t.Fatal(err)
	}

	notes := s.TaskNoteContent("01TASK")
	if strings.Count(notes, "root cause is in adf.go") != 1 {
		t.Fatalf("dedup failed:\n%s", notes)
	}
	first := strings.Index(notes, "root cause")
	second := strings.Index(notes, "fix lives")
	if first < 0 || second < 0 || first > second {
		t.Fatalf("notes out of order:\n%s", notes)
	}
	if strings.Contains(notes, lastHashPrefix) {
		t.Fatalf("hash marker leaked into content:\n%s", notes)
	}
}

func TestRebuildIndex_SelfHealsAfterManualDelete(t *testing.T) {
	s := testStore(t)
	for _, n := range []string{"alpha", "beta"} {
		if err := s.UpsertGlobal(Entry{Name: n, Description: n + " fact", Content: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	// Operator deletes a file by hand; index is stale until the next write/open.
	if err := os.Remove(filepath.Join(s.Root(), "global", "alpha.md")); err != nil {
		t.Fatal(err)
	}
	if err := s.RebuildIndex(); err != nil {
		t.Fatal(err)
	}
	idx, _ := os.ReadFile(filepath.Join(s.Root(), IndexFile))
	if strings.Contains(string(idx), "alpha") || !strings.Contains(string(idx), "beta") {
		t.Fatalf("index not healed: %s", idx)
	}
}

func TestDelete(t *testing.T) {
	s := testStore(t)
	if err := s.UpsertGlobal(Entry{Name: "gone", Description: "d", Content: "c"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("gone"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read("gone"); err == nil {
		t.Fatal("entry still readable after delete")
	}
	idx, _ := os.ReadFile(filepath.Join(s.Root(), IndexFile))
	if strings.Contains(string(idx), "gone") {
		t.Fatalf("index still lists deleted entry: %s", idx)
	}
}

func TestRenderRecall_SectionsAndTiers(t *testing.T) {
	s := testStore(t)
	if err := s.UpsertGlobal(Entry{Name: "fact-a", Description: "alpha fact", Content: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendTaskNote("01SELF", Note{Content: "decided X"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendTaskNote("01PARENT", Note{Content: "collected Y"}); err != nil {
		t.Fatal(err)
	}

	out := s.RenderRecall([]string{"01SELF", "01PARENT"}, []string{TierTask, TierGlobal}, 4000)
	for _, want := range []string{"[Long-term Memory]", "fact-a — alpha fact", "[Task Memory]", "decided X", "from parent task 01PARENT", "collected Y"} {
		if !strings.Contains(out, want) {
			t.Fatalf("recall missing %q:\n%s", want, out)
		}
	}

	// Tier filtering: task only.
	out = s.RenderRecall([]string{"01SELF"}, []string{TierTask}, 4000)
	if strings.Contains(out, "[Long-term Memory]") || !strings.Contains(out, "[Task Memory]") {
		t.Fatalf("tier filter failed:\n%s", out)
	}

	// Empty store / unknown task → zero overhead.
	empty := testStore(t)
	if got := empty.RenderRecall([]string{"none"}, []string{TierTask, TierGlobal}, 4000); got != "" {
		t.Fatalf("expected empty recall, got:\n%s", got)
	}
}

func TestRenderRecall_BudgetTruncation(t *testing.T) {
	s := testStore(t)
	for _, n := range []string{"aaa-one", "bbb-two", "ccc-three", "ddd-four"} {
		if err := s.UpsertGlobal(Entry{Name: n, Description: strings.Repeat("d", 80), Content: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	out := s.RenderRecall(nil, []string{TierGlobal}, 700)
	if !strings.Contains(out, "more entries") {
		t.Fatalf("expected truncation marker:\n%s", out)
	}

	// Task notes drop oldest-first under budget.
	for i := 0; i < 30; i++ {
		if err := s.AppendTaskNote("01BIG", Note{Content: strings.Repeat("n", 50) + string(rune('a'+i))}); err != nil {
			t.Fatal(err)
		}
	}
	out = s.RenderRecall([]string{"01BIG"}, []string{TierTask}, 400)
	if out == "" {
		t.Fatal("expected truncated task notes, got empty")
	}
	if len(out) > 400 {
		t.Fatalf("over budget: %d", len(out))
	}
}

func TestListTaskNotesAndDelete(t *testing.T) {
	s := testStore(t)
	if err := s.AppendTaskNote("01A", Note{Content: "x"}); err != nil {
		t.Fatal(err)
	}
	notes, err := s.ListTaskNotes()
	if err != nil || len(notes) != 1 || notes[0].TaskID != "01A" {
		t.Fatalf("ListTaskNotes: %v %v", notes, err)
	}
	if err := s.DeleteTaskNotes("01A"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTaskNotes("01A"); err != nil {
		t.Fatalf("delete absent should be no-op: %v", err)
	}
}
