package db

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
)

func TestInternalTask_CRUD(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	store := c.InternalTasks()

	task := &model.InternalTask{
		Title:       "Investigate payments incident",
		Description: "critical alert from log monitor",
		Input:       map[string]any{"service": "payments", "severity": "critical"},
		Metadata: model.TaskMetadata{
			Labels:   []string{"apiary", "incident"},
			Priority: "high",
			Type:     "log_event",
		},
	}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatalf("create: %v", err)
	}
	if task.ID == "" {
		t.Fatal("expected generated ID, got empty")
	}
	if task.State != model.TaskStateRegistered {
		t.Errorf("default state = %q, want registered", task.State)
	}

	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected task, got nil")
	}
	if got.Title != task.Title || got.Description != task.Description {
		t.Errorf("scalar fields wrong: %+v", got)
	}
	if got.Input["service"] != "payments" || got.Input["severity"] != "critical" {
		t.Errorf("input round-trip wrong: %#v", got.Input)
	}
	if got.Metadata.Priority != "high" || got.Metadata.Type != "log_event" ||
		len(got.Metadata.Labels) != 2 {
		t.Errorf("metadata round-trip wrong: %#v", got.Metadata)
	}
}

func TestInternalTask_FindChildByDedupKey(t *testing.T) {
	ctx := context.Background()
	store := newTestClient(t).InternalTasks()

	parent := &model.InternalTask{Title: "spec"}
	if err := store.CreateTask(ctx, parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}

	child := &model.InternalTask{
		ParentTaskID: parent.ID,
		Title:        "DB Migration",
		DedupKey:     "spec/db",
	}
	if err := store.CreateTask(ctx, child); err != nil {
		t.Fatalf("create child: %v", err)
	}

	got, err := store.FindChildByDedupKey(ctx, parent.ID, "spec/db")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got == nil || got.ID != child.ID {
		t.Fatalf("FindChildByDedupKey returned %+v, want child %s", got, child.ID)
	}

	// Empty key never matches; a different key under the same parent misses.
	if g, _ := store.FindChildByDedupKey(ctx, parent.ID, ""); g != nil {
		t.Errorf("empty key should not match, got %+v", g)
	}
	if g, _ := store.FindChildByDedupKey(ctx, parent.ID, "spec/other"); g != nil {
		t.Errorf("unknown key should not match, got %+v", g)
	}
}

func TestInternalTask_DedupKeyUniquePerParent(t *testing.T) {
	ctx := context.Background()
	store := newTestClient(t).InternalTasks()

	parent := &model.InternalTask{Title: "spec"}
	if err := store.CreateTask(ctx, parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}

	first := &model.InternalTask{ParentTaskID: parent.ID, Title: "Backend", DedupKey: "spec/backend"}
	if err := store.CreateTask(ctx, first); err != nil {
		t.Fatalf("create first: %v", err)
	}
	// A second child with the same (parent, dedup_key) must be rejected by the
	// partial unique index — this is what stops the duplicate fan-out (issue #119).
	dup := &model.InternalTask{ParentTaskID: parent.ID, Title: "Backend", DedupKey: "spec/backend"}
	if err := store.CreateTask(ctx, dup); err == nil {
		t.Fatal("expected unique-constraint error for duplicate (parent, dedup_key), got nil")
	}

	// Empty dedup keys are exempt: multiple source-bound children may share a
	// parent with no key.
	for i := 0; i < 2; i++ {
		if err := store.CreateTask(ctx, &model.InternalTask{ParentTaskID: parent.ID, Title: "no-key"}); err != nil {
			t.Fatalf("empty-key child %d should be allowed: %v", i, err)
		}
	}
}

func TestInternalTask_GetMissing(t *testing.T) {
	ctx := context.Background()
	store := newTestClient(t).InternalTasks()
	got, err := store.GetTask(ctx, "does-not-exist")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing task, got %+v", got)
	}
}

func TestInternalTask_NilInputStoredAsNull(t *testing.T) {
	ctx := context.Background()
	store := newTestClient(t).InternalTasks()
	task := &model.InternalTask{Title: "no input"}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Input != nil {
		t.Errorf("expected nil Input, got %#v", got.Input)
	}
}

func TestInternalTask_UpdateState(t *testing.T) {
	ctx := context.Background()
	store := newTestClient(t).InternalTasks()
	task := &model.InternalTask{Title: "t"}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.UpdateTaskState(ctx, task.ID, model.TaskStateRunning); err != nil {
		t.Fatalf("update state: %v", err)
	}
	got, _ := store.GetTask(ctx, task.ID)
	if got.State != model.TaskStateRunning {
		t.Errorf("state = %q, want running", got.State)
	}
}

// TestInternalTask_DecrementOutstandingConcurrent locks in the race-free
// guarantee the engine's completion hook relies on: when N sibling instances of
// one task settle concurrently, the N atomic UPDATE ... RETURNING decrements each
// return a distinct post-decrement count (the full 0..N-1 set), so exactly one
// caller observes zero and fires the hook once. A non-atomic update-then-select
// would let two callers both read zero.
func TestInternalTask_DecrementOutstandingConcurrent(t *testing.T) {
	ctx := context.Background()
	store := newTestClient(t).InternalTasks()
	task := &model.InternalTask{Title: "t"}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatalf("create: %v", err)
	}

	const n = 16
	if _, err := store.IncrementOutstanding(ctx, task.ID, n); err != nil {
		t.Fatalf("increment: %v", err)
	}

	results := make([]int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := store.DecrementOutstanding(ctx, task.ID)
			if err != nil {
				t.Errorf("decrement: %v", err)
				return
			}
			results[i] = got
		}(i)
	}
	wg.Wait()

	seen := make(map[int]int)
	for _, r := range results {
		seen[r]++
	}
	for v := 0; v < n; v++ {
		if seen[v] != 1 {
			t.Fatalf("count %d returned %d time(s), want exactly 1 — results=%v", v, seen[v], results)
		}
	}
}

func TestInternalTask_OutstandingCounter(t *testing.T) {
	ctx := context.Background()
	store := newTestClient(t).InternalTasks()
	task := &model.InternalTask{Title: "t"}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatalf("create: %v", err)
	}

	n, err := store.IncrementOutstanding(ctx, task.ID, 3)
	if err != nil {
		t.Fatalf("increment: %v", err)
	}
	if n != 3 {
		t.Errorf("after +3, count = %d, want 3", n)
	}

	for i, want := range []int{2, 1, 0} {
		n, err := store.DecrementOutstanding(ctx, task.ID)
		if err != nil {
			t.Fatalf("decrement %d: %v", i, err)
		}
		if n != want {
			t.Errorf("decrement %d: count = %d, want %d", i, n, want)
		}
	}

	// Clamped at zero — never negative.
	n, err = store.DecrementOutstanding(ctx, task.ID)
	if err != nil {
		t.Fatalf("decrement past zero: %v", err)
	}
	if n != 0 {
		t.Errorf("decrement past zero: count = %d, want 0", n)
	}
}

// TestInternalTask_IncrementReopensTerminal locks in that a re-dispatch reopens a
// finished task. Multi-stage pipelines fan out across separate poll cycles, so the
// counter drains to zero and the task is marked terminal between stages; the next
// stage's IncrementOutstanding must flip 'done'/'failed' back to 'running' so the
// task list does not read terminal while a later instance is still in flight.
func TestInternalTask_IncrementReopensTerminal(t *testing.T) {
	ctx := context.Background()
	store := newTestClient(t).InternalTasks()

	for _, terminal := range []model.TaskState{model.TaskStateDone, model.TaskStateFailed} {
		task := &model.InternalTask{Title: "t", State: terminal}
		if err := store.CreateTask(ctx, task); err != nil {
			t.Fatalf("create (%s): %v", terminal, err)
		}
		if _, err := store.IncrementOutstanding(ctx, task.ID, 1); err != nil {
			t.Fatalf("increment (%s): %v", terminal, err)
		}
		got, _ := store.GetTask(ctx, task.ID)
		if got.State != model.TaskStateRunning {
			t.Errorf("reopen from %s: state = %q, want running", terminal, got.State)
		}
	}

	// A non-terminal state is left untouched — increment must not clobber an
	// active task's lifecycle (e.g. approval_waiting).
	for _, keep := range []model.TaskState{model.TaskStateRegistered, model.TaskStateApprovalWait} {
		task := &model.InternalTask{Title: "t", State: keep}
		if err := store.CreateTask(ctx, task); err != nil {
			t.Fatalf("create (%s): %v", keep, err)
		}
		if _, err := store.IncrementOutstanding(ctx, task.ID, 2); err != nil {
			t.Fatalf("increment (%s): %v", keep, err)
		}
		got, _ := store.GetTask(ctx, task.ID)
		if got.State != keep {
			t.Errorf("increment must preserve %q, got %q", keep, got.State)
		}
	}
}

func TestInternalTask_ListByState(t *testing.T) {
	ctx := context.Background()
	store := newTestClient(t).InternalTasks()

	for i := 0; i < 3; i++ {
		if err := store.CreateTask(ctx, &model.InternalTask{
			Title: "running", State: model.TaskStateRunning,
		}); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	if err := store.CreateTask(ctx, &model.InternalTask{
		Title: "done", State: model.TaskStateDone,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	running, err := store.ListTasksByState(ctx, model.TaskStateRunning)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(running) != 3 {
		t.Errorf("running count = %d, want 3", len(running))
	}
	done, _ := store.ListTasksByState(ctx, model.TaskStateDone)
	if len(done) != 1 {
		t.Errorf("done count = %d, want 1", len(done))
	}
}

func TestSourceBinding_CRUD(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	tasks := c.InternalTasks()
	bindings := c.SourceBindings()

	task := &model.InternalTask{Title: "bound task"}
	if err := tasks.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	b := &model.SourceBinding{
		TaskID:           task.ID,
		SourceID:         "github",
		SourceItemID:     "12345",
		SourceItemURL:    "https://github.com/o/r/issues/42",
		SourceItemNumber: "#42",
	}
	if err := bindings.CreateBinding(ctx, b); err != nil {
		t.Fatalf("create binding: %v", err)
	}
	if b.ID == "" {
		t.Fatal("expected generated binding ID")
	}

	got, err := bindings.GetBindingBySourceItem(ctx, "github", "12345")
	if err != nil {
		t.Fatalf("get binding: %v", err)
	}
	if got == nil {
		t.Fatal("expected binding, got nil")
	}
	if got.TaskID != task.ID || got.SourceItemNumber != "#42" {
		t.Errorf("binding fields wrong: %+v", got)
	}

	list, err := bindings.ListBindingsByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("list bindings: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("bindings count = %d, want 1", len(list))
	}
}

func TestSourceBinding_GetMissing(t *testing.T) {
	ctx := context.Background()
	bindings := newTestClient(t).SourceBindings()
	got, err := bindings.GetBindingBySourceItem(ctx, "github", "nope")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing binding, got %+v", got)
	}
}

func TestSourceBinding_UniqueConstraint(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	task := &model.InternalTask{Title: "t"}
	if err := c.InternalTasks().CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	bindings := c.SourceBindings()
	first := &model.SourceBinding{TaskID: task.ID, SourceID: "github", SourceItemID: "1"}
	if err := bindings.CreateBinding(ctx, first); err != nil {
		t.Fatalf("first binding: %v", err)
	}
	dup := &model.SourceBinding{TaskID: task.ID, SourceID: "github", SourceItemID: "1"}
	if err := bindings.CreateBinding(ctx, dup); err == nil {
		t.Error("expected unique-constraint error on duplicate (source_id, source_item_id)")
	}
}

func TestInternalTask_ListTasks(t *testing.T) {
	ctx := context.Background()
	store := newTestClient(t).InternalTasks()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Three tasks in different states, inserted with increasing created_at.
	for i, st := range []model.TaskState{model.TaskStateRegistered, model.TaskStateRunning, model.TaskStateDone} {
		task := &model.InternalTask{
			Title:     "t" + string(rune('A'+i)),
			State:     st,
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}
		if err := store.CreateTask(ctx, task); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	got, err := store.ListTasks(ctx, 100)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListTasks returned %d tasks, want 3 (across all states)", len(got))
	}
	// Newest first: tC (done) before tB (running) before tA (registered).
	if got[0].Title != "tC" || got[1].Title != "tB" || got[2].Title != "tA" {
		t.Errorf("ListTasks order = [%s %s %s], want newest-first [tC tB tA]", got[0].Title, got[1].Title, got[2].Title)
	}

	// limit is honored.
	lim, err := store.ListTasks(ctx, 2)
	if err != nil {
		t.Fatalf("ListTasks(2): %v", err)
	}
	if len(lim) != 2 || lim[0].Title != "tC" {
		t.Errorf("ListTasks(2) = %d rows starting %q, want 2 starting tC", len(lim), titleAt(lim, 0))
	}
}

func TestInternalTask_ListChildTasks(t *testing.T) {
	ctx := context.Background()
	store := newTestClient(t).InternalTasks()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	parent := &model.InternalTask{Title: "parent", CreatedAt: base}
	if err := store.CreateTask(ctx, parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	other := &model.InternalTask{Title: "other", CreatedAt: base}
	if err := store.CreateTask(ctx, other); err != nil {
		t.Fatalf("create other: %v", err)
	}
	// Two children of parent (oldest-first expected), one child of other.
	c1 := &model.InternalTask{Title: "child1", ParentTaskID: parent.ID, CreatedAt: base.Add(1 * time.Minute)}
	c2 := &model.InternalTask{Title: "child2", ParentTaskID: parent.ID, CreatedAt: base.Add(2 * time.Minute)}
	c3 := &model.InternalTask{Title: "child3", ParentTaskID: other.ID, CreatedAt: base.Add(3 * time.Minute)}
	for _, c := range []*model.InternalTask{c1, c2, c3} {
		if err := store.CreateTask(ctx, c); err != nil {
			t.Fatalf("create child: %v", err)
		}
	}

	kids, err := store.ListChildTasks(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListChildTasks: %v", err)
	}
	if len(kids) != 2 || kids[0].Title != "child1" || kids[1].Title != "child2" {
		t.Fatalf("ListChildTasks(parent) = %d %v, want [child1 child2] oldest-first", len(kids), titles(kids))
	}
	if none, _ := store.ListChildTasks(ctx, parent.ID+"-missing"); len(none) != 0 {
		t.Errorf("ListChildTasks(unknown) = %d, want 0", len(none))
	}
}

func TestInternalTask_GetTaskAncestors(t *testing.T) {
	ctx := context.Background()
	store := newTestClient(t).InternalTasks()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Chain: root -> mid -> leaf
	root := &model.InternalTask{Title: "root", CreatedAt: base}
	if err := store.CreateTask(ctx, root); err != nil {
		t.Fatalf("create root: %v", err)
	}
	mid := &model.InternalTask{Title: "mid", ParentTaskID: root.ID, CreatedAt: base.Add(time.Minute)}
	if err := store.CreateTask(ctx, mid); err != nil {
		t.Fatalf("create mid: %v", err)
	}
	leaf := &model.InternalTask{Title: "leaf", ParentTaskID: mid.ID, CreatedAt: base.Add(2 * time.Minute)}
	if err := store.CreateTask(ctx, leaf); err != nil {
		t.Fatalf("create leaf: %v", err)
	}

	anc, err := store.GetTaskAncestors(ctx, leaf.ID)
	if err != nil {
		t.Fatalf("GetTaskAncestors: %v", err)
	}
	// Root first, leaf last.
	if len(anc) != 3 || anc[0].Title != "root" || anc[1].Title != "mid" || anc[2].Title != "leaf" {
		t.Fatalf("GetTaskAncestors(leaf) = %v, want root-first [root mid leaf]", titles(anc))
	}

	// A root task is its own only ancestor.
	rootAnc, err := store.GetTaskAncestors(ctx, root.ID)
	if err != nil {
		t.Fatalf("GetTaskAncestors(root): %v", err)
	}
	if len(rootAnc) != 1 || rootAnc[0].Title != "root" {
		t.Errorf("GetTaskAncestors(root) = %v, want [root]", titles(rootAnc))
	}

	// Unknown id yields no rows.
	if none, _ := store.GetTaskAncestors(ctx, "nope"); len(none) != 0 {
		t.Errorf("GetTaskAncestors(unknown) = %d, want 0", len(none))
	}
}

func titles(ts []model.InternalTask) []string {
	out := make([]string, len(ts))
	for i := range ts {
		out[i] = ts[i].Title
	}
	return out
}

func titleAt(ts []model.InternalTask, i int) string {
	if i < len(ts) {
		return ts[i].Title
	}
	return ""
}

func TestInternalTask_ListTasksPage_Walk(t *testing.T) {
	ctx := context.Background()
	store := newTestClient(t).InternalTasks()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Five tasks: t0..t4 with increasing created_at, except t3 and t4 share a
	// timestamp so the id tie-breaker is exercised.
	for i := 0; i < 5; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		if i == 4 {
			at = base.Add(3 * time.Minute)
		}
		task := &model.InternalTask{ID: "tk_" + string(rune('0'+i)), Title: "t" + string(rune('0'+i)), CreatedAt: at}
		if err := store.CreateTask(ctx, task); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	// Walk the whole list two rows at a time, continuing from each page's last row.
	first, err := store.ListTasks(ctx, 2)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(first) != 2 || first[0].Title != "t4" || first[1].Title != "t3" {
		t.Fatalf("first page = %v, want [t4 t3] (tie broken by id desc)", titles(first))
	}
	var seen []string
	page := first
	for len(page) > 0 {
		for _, tk := range page {
			seen = append(seen, tk.Title)
		}
		last := page[len(page)-1]
		page, err = store.ListTasksPage(ctx, TaskListFilter{}, TaskCursor{CreatedAt: last.CreatedAt, ID: last.ID}, 2)
		if err != nil {
			t.Fatalf("ListTasksPage: %v", err)
		}
	}
	want := []string{"t4", "t3", "t2", "t1", "t0"}
	if strings.Join(seen, " ") != strings.Join(want, " ") {
		t.Errorf("paged walk = %v, want %v (no gaps, no repeats)", seen, want)
	}

	// Past the oldest row there is nothing left.
	empty, err := store.ListTasksPage(ctx, TaskListFilter{}, TaskCursor{CreatedAt: base, ID: "tk_0"}, 2)
	if err != nil {
		t.Fatalf("ListTasksPage(oldest): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("page past the oldest row = %v, want empty", titles(empty))
	}
}

// The filters run in the query, so they match rows far beyond the first page.
func TestInternalTask_ListTasksPage_Filters(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	store := c.InternalTasks()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	mk := func(id, title string, st model.TaskState, reason string, offset int) {
		task := &model.InternalTask{ID: id, Title: title, State: st, BlockedReason: reason, CreatedAt: base.Add(time.Duration(offset) * time.Minute)}
		if err := store.CreateTask(ctx, task); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	// Oldest first so the interesting rows sit at the tail of an unfiltered list.
	mk("tk_old", "Fix login 100% of the time", model.TaskStateDone, "", 0)
	mk("tk_gate", "Ship it", model.TaskStateBlocked, "approval", 1)
	// CreateTask does not persist blocked_reason; park the gate the way the engine does.
	if err := store.UpdateTaskStateReason(ctx, "tk_gate", model.TaskStateBlocked, "approval"); err != nil {
		t.Fatalf("park tk_gate: %v", err)
	}
	mk("tk_routine", "nightly sweep", model.TaskStateDone, "", 2)
	for i := 0; i < 5; i++ {
		mk("tk_fill"+string(rune('0'+i)), "filler", model.TaskStateRunning, "", 10+i)
	}
	bind := func(taskID, sourceID, itemID, number string) {
		if err := c.SourceBindings().CreateBinding(ctx, &model.SourceBinding{TaskID: taskID, SourceID: sourceID, SourceItemID: itemID, SourceItemNumber: number}); err != nil {
			t.Fatalf("bind %s: %v", taskID, err)
		}
	}
	bind("tk_old", "github", "ISSUE-9", "#9")
	bind("tk_routine", "cron", "nightly@2026-01-01", "")

	list := func(f TaskListFilter) []string {
		t.Helper()
		got, err := store.ListTasksPage(ctx, f, TaskCursor{}, 2) // a page smaller than the table
		if err != nil {
			t.Fatalf("ListTasksPage(%+v): %v", f, err)
		}
		return titles(got)
	}
	if got := list(TaskListFilter{}); strings.Join(got, " ") != "filler filler" {
		t.Fatalf("unfiltered first page = %v, want two fillers", got)
	}
	// Text: title, case-insensitive, beyond the first page; LIKE wildcards literal.
	if got := list(TaskListFilter{Text: "LOGIN"}); strings.Join(got, " ") != "Fix login 100% of the time" {
		t.Errorf("text(title) = %v", got)
	}
	if got := list(TaskListFilter{Text: "100%"}); len(got) != 1 {
		t.Errorf("text with %% should be literal, got %v", got)
	}
	if got := list(TaskListFilter{Text: "0_o"}); len(got) != 0 {
		t.Errorf("text with _ should be literal, got %v", got)
	}
	// Text: binding item id / number, task id, state.
	if got := list(TaskListFilter{Text: "#9"}); strings.Join(got, " ") != "Fix login 100% of the time" {
		t.Errorf("text(number) = %v", got)
	}
	if got := list(TaskListFilter{Text: "issue-9"}); len(got) != 1 {
		t.Errorf("text(item id) = %v", got)
	}
	if got := list(TaskListFilter{Text: "tk_gate"}); strings.Join(got, " ") != "Ship it" {
		t.Errorf("text(task id) = %v", got)
	}
	if got := list(TaskListFilter{Text: "blocked"}); strings.Join(got, " ") != "Ship it" {
		t.Errorf("text(state) = %v", got)
	}
	// Approvals only.
	if got := list(TaskListFilter{ApprovalsOnly: true}); strings.Join(got, " ") != "Ship it" {
		t.Errorf("approvals = %v", got)
	}
	// Tickets only: bound to a source that is not on the non-ticket list.
	if got := list(TaskListFilter{TicketsOnly: true, NonTicketSources: []string{"cron"}}); strings.Join(got, " ") != "Fix login 100% of the time" {
		t.Errorf("tickets(excluding cron) = %v", got)
	}
	if got := list(TaskListFilter{TicketsOnly: true}); strings.Join(got, " ") != "nightly sweep Fix login 100% of the time" {
		t.Errorf("tickets(no exclusions) = %v", got)
	}
	// Filters combine, and the cursor applies within the filtered set.
	got, err := store.ListTasksPage(ctx, TaskListFilter{Text: "filler"}, TaskCursor{}, 3)
	if err != nil || len(got) != 3 {
		t.Fatalf("filtered page: %v %v", got, err)
	}
	last := got[2]
	next, err := store.ListTasksPage(ctx, TaskListFilter{Text: "filler"}, TaskCursor{CreatedAt: last.CreatedAt, ID: last.ID}, 3)
	if err != nil || len(next) != 2 || next[0].ID == last.ID {
		t.Errorf("filtered next page = %v (%v), want the remaining two fillers", titles(next), err)
	}
}
