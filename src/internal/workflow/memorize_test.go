package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/memory"
	"github.com/orlandoburli/apiary/internal/model"
)

// memEngine builds a test engine wired to a real memory.Store in a temp dir.
func memEngine(t *testing.T, cfg *config.Config, store Store, exec StepExecutor) (*Engine, *memory.Store) {
	t.Helper()
	ms, err := memory.Open(t.TempDir())
	if err != nil {
		t.Fatalf("memory.Open: %v", err)
	}
	e := testEngine(cfg, store, exec, &fakeSide{})
	WithMemoryStore(ms)(e)
	return e, ms
}

func memCfg() *config.Config {
	cfg := baseCfg()
	cfg.Settings.Memory = config.MemorySettings{Enabled: true, MaxInjectChars: 4000, MaxEntryBytes: 16384}
	return cfg
}

func TestEngine_MemorizeGlobalAndTask(t *testing.T) {
	cfg := memCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"run": {Success: true, Output: "done", MemorizeRequests: []model.MemorizeRequest{
			{Scope: "global", Name: "ci-duration", Description: "CI takes 12m", Content: "plan accordingly"},
			{Content: "decided to use lipgloss"}, // scope defaults to task
		}},
	}}
	e, ms := memEngine(t, cfg, store, exec)

	wf := config.WorkflowConfig{ID: "engineer", Steps: []config.StepConfig{{ID: "run", Agent: "backend-dev"}}}
	task := model.InternalTask{ID: "01TASK", Title: "Fix bug"}
	if _, ok, err := e.RunInstance(context.Background(), wf, task); err != nil || !ok {
		t.Fatalf("RunInstance: ok=%v err=%v", ok, err)
	}

	entry, err := ms.Read("ci-duration")
	if err != nil {
		t.Fatalf("global entry not written: %v", err)
	}
	if entry.Agent != "backend-dev" || entry.Task != "01TASK" || entry.Workflow != "engineer" {
		t.Fatalf("provenance wrong: %+v", entry)
	}
	notes := ms.TaskNoteContent("01TASK")
	if !strings.Contains(notes, "decided to use lipgloss") {
		t.Fatalf("task note not written:\n%s", notes)
	}
	if !strings.Contains(notes, "[engineer/run]") || !strings.Contains(notes, "(backend-dev)") {
		t.Fatalf("note provenance missing:\n%s", notes)
	}
}

func TestEngine_MemorizeInvalidNeverFailsStep(t *testing.T) {
	cfg := memCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"run": {Success: true, Output: "done", MemorizeRequests: []model.MemorizeRequest{
			{Scope: "global", Content: "no name or description"}, // rejected by the store
			{Scope: "nonsense", Content: "bad scope"},            // rejected by the engine
		}},
	}}
	e, ms := memEngine(t, cfg, store, exec)

	wf := config.WorkflowConfig{ID: "r", Steps: []config.StepConfig{{ID: "run", Agent: "backend-dev"}}}
	_, ok, err := e.RunInstance(context.Background(), wf, model.InternalTask{ID: "01T"})
	if err != nil || !ok {
		t.Fatalf("invalid memorize must not fail the step: ok=%v err=%v", ok, err)
	}
	if metas, _ := ms.List(); len(metas) != 0 {
		t.Fatalf("rejected requests were persisted: %v", metas)
	}
}

func TestEngine_MemorizeDisabledIsNoop(t *testing.T) {
	cfg := baseCfg() // memory disabled — engine has no store wired
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"run": {Success: true, MemorizeRequests: []model.MemorizeRequest{{Content: "note"}}},
	}}
	e := testEngine(cfg, store, exec, &fakeSide{})

	wf := config.WorkflowConfig{ID: "r", Steps: []config.StepConfig{{ID: "run", Agent: "backend-dev"}}}
	if _, ok, err := e.RunInstance(context.Background(), wf, model.InternalTask{ID: "01T"}); err != nil || !ok {
		t.Fatalf("RunInstance: ok=%v err=%v", ok, err)
	}
}

func TestEngine_RecallInjectedIntoMemoryDoc(t *testing.T) {
	cfg := memCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{"run": {Success: true}}}
	e, ms := memEngine(t, cfg, store, exec)

	if err := ms.UpsertGlobal(memory.Entry{Name: "known-fact", Description: "a durable fact", Content: "details"}); err != nil {
		t.Fatal(err)
	}
	if err := ms.AppendTaskNote("01TASK", memory.Note{Content: "earlier instance decided X"}); err != nil {
		t.Fatal(err)
	}

	wf := config.WorkflowConfig{ID: "r", Steps: []config.StepConfig{{ID: "run", Agent: "backend-dev"}}}
	if _, ok, err := e.RunInstance(context.Background(), wf, model.InternalTask{ID: "01TASK", Title: "T"}); err != nil || !ok {
		t.Fatalf("RunInstance: ok=%v err=%v", ok, err)
	}

	doc := exec.seen[0].MemoryDoc
	for _, want := range []string{"[Long-term Memory]", "known-fact — a durable fact", "[Task Memory]", "earlier instance decided X", "=== Workflow Memory ==="} {
		if !strings.Contains(doc, want) {
			t.Fatalf("memory doc missing %q:\n%s", want, doc)
		}
	}
	// Recall rides ahead of the per-instance document.
	if strings.Index(doc, "[Long-term Memory]") > strings.Index(doc, "=== Workflow Memory ===") {
		t.Fatalf("recall not prepended:\n%s", doc)
	}
}

func TestEngine_RecallRespectsTierFilterAndReadOff(t *testing.T) {
	cfg := memCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{"a": {Success: true}, "b": {Success: true}}}
	e, ms := memEngine(t, cfg, store, exec)

	if err := ms.UpsertGlobal(memory.Entry{Name: "known-fact", Description: "d", Content: "c"}); err != nil {
		t.Fatal(err)
	}
	if err := ms.AppendTaskNote("01TASK", memory.Note{Content: "task context"}); err != nil {
		t.Fatal(err)
	}

	off := false
	wf := config.WorkflowConfig{ID: "r", Steps: []config.StepConfig{
		{ID: "a", Agent: "backend-dev", Memory: &config.MemoryConfig{Recall: []string{"task"}}},
		{ID: "b", Agent: "backend-dev", Memory: &config.MemoryConfig{Read: &off}},
	}}
	if _, ok, err := e.RunInstance(context.Background(), wf, model.InternalTask{ID: "01TASK"}); err != nil || !ok {
		t.Fatalf("RunInstance: ok=%v err=%v", ok, err)
	}

	docA := exec.seen[0].MemoryDoc
	if strings.Contains(docA, "[Long-term Memory]") || !strings.Contains(docA, "task context") {
		t.Fatalf("recall tier filter failed:\n%s", docA)
	}
	docB := exec.seen[1].MemoryDoc
	if docB != "" {
		t.Fatalf("memory.read: false must suppress everything, got:\n%s", docB)
	}
}

func TestEngine_RecallInheritsAncestorNotes(t *testing.T) {
	cfg := memCfg()
	store := newFakeStore()
	store.ancestors = map[string][]model.InternalTask{
		// GetTaskAncestors order: root first, self last.
		"01CHILD": {{ID: "01ROOT"}, {ID: "01PARENT"}, {ID: "01CHILD"}},
	}
	exec := &fakeExecutor{results: map[string]StepResult{"run": {Success: true}}}
	e, ms := memEngine(t, cfg, store, exec)

	if err := ms.AppendTaskNote("01PARENT", memory.Note{Content: "parent collected logs"}); err != nil {
		t.Fatal(err)
	}
	if err := ms.AppendTaskNote("01CHILD", memory.Note{Content: "child started"}); err != nil {
		t.Fatal(err)
	}

	wf := config.WorkflowConfig{ID: "r", Steps: []config.StepConfig{{ID: "run", Agent: "backend-dev"}}}
	if _, ok, err := e.RunInstance(context.Background(), wf, model.InternalTask{ID: "01CHILD"}); err != nil || !ok {
		t.Fatalf("RunInstance: ok=%v err=%v", ok, err)
	}

	doc := exec.seen[0].MemoryDoc
	if !strings.Contains(doc, "parent collected logs") || !strings.Contains(doc, "from parent task 01PARENT") {
		t.Fatalf("ancestor notes not inherited:\n%s", doc)
	}
	// Self notes come before the parent's.
	if strings.Index(doc, "child started") > strings.Index(doc, "parent collected logs") {
		t.Fatalf("self notes must lead:\n%s", doc)
	}
}

func TestEngine_MemorizeRetryDedups(t *testing.T) {
	cfg := memCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"run": {Success: true, MemorizeRequests: []model.MemorizeRequest{{Content: "same note"}}},
	}}
	e, ms := memEngine(t, cfg, store, exec)

	wf := config.WorkflowConfig{ID: "r", Steps: []config.StepConfig{{ID: "run", Agent: "backend-dev"}}}
	for i := 0; i < 2; i++ { // a re-dispatched instance re-emits the same note
		if _, ok, err := e.RunInstance(context.Background(), wf, model.InternalTask{ID: "01T"}); err != nil || !ok {
			t.Fatalf("run %d: ok=%v err=%v", i, ok, err)
		}
	}
	if n := strings.Count(ms.TaskNoteContent("01T"), "same note"); n != 1 {
		t.Fatalf("expected 1 note after retry, found %d", n)
	}
}

// TestMemoryStoreSatisfiedByRealStore pins the interface compatibility.
func TestMemoryStoreSatisfiedByRealStore(t *testing.T) {
	var _ MemoryStore = (*memory.Store)(nil)
	// And the entry files land where APIARY_MEMORY_DIR points.
	ms, err := memory.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := ms.UpsertGlobal(memory.Entry{Name: "loc", Description: "d", Content: "c"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ms.Root(), "global", "loc.md")); err != nil {
		t.Fatalf("entry not on disk: %v", err)
	}
}
