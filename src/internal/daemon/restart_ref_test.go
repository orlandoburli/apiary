package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

// seedJiraTask seeds the shape that made restart unusable on Jira: the binding's
// item id is the opaque numeric issue id, while the only reference a human ever
// sees is the key.
func seedJiraTask(ctx context.Context, t *testing.T, dbc *db.Client, itemID, key string) string {
	t.Helper()
	task := &model.InternalTask{Title: "Fix the thing", State: model.TaskStateRunning}
	binding := &model.SourceBinding{SourceID: "jira", SourceItemID: itemID, SourceItemNumber: key}
	if err := dbc.CreateTaskWithBinding(ctx, task, binding); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	inst := &db.WorkflowInstance{
		ID: "wf_" + itemID, WorkflowID: "eng", TaskID: task.ID,
		CellID: itemID, SourceID: "jira", State: db.InstanceStateRunning,
	}
	if err := dbc.CreateWorkflowInstance(ctx, inst); err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	return task.ID
}

// TestForceRestart_AcceptsJiraKey is the case that motivated reference
// resolution: the Jira adapter binds on the numeric issue id (10042), so the key
// the user reads everywhere — CDT-123 — was rejected as an unknown cell, and the
// id that did work appeared in no interface at all.
func TestForceRestart_AcceptsJiraKey(t *testing.T) {
	ctx := context.Background()
	dbc := openTestDB(ctx, t)
	seedJiraTask(ctx, t, dbc, "10042", "CDT-123")

	fake := &countingSource{cell: model.SourceItem{ID: "10042", Labels: []string{"in-progress"}}}
	d := restartUnknownDispatcher(dbc, fake)

	res, err := d.ForceRestart(ctx, "CDT-123")
	if err != nil {
		t.Fatalf("ForceRestart(%q): %v", "CDT-123", err)
	}
	if res.CellID != "10042" {
		t.Errorf("resolved cell id = %q, want 10042", res.CellID)
	}
	if res.Ref != "CDT-123" {
		t.Errorf("Ref = %q, want CDT-123", res.Ref)
	}
	// The label is what surfaces in the CLI and the dashboard banner.
	if got := res.Label(); !strings.Contains(got, "CDT-123") {
		t.Errorf("Label() = %q, want it to lead with the key the user typed", got)
	}
	// And it restarted the right item: the state reset reached the source.
	if len(fake.statesSet) == 0 {
		t.Error("restart did not reach the source — the key resolved to nothing")
	}
}

// TestForceRestart_JiraKeyIsCaseInsensitive: people type cdt-123.
func TestForceRestart_JiraKeyIsCaseInsensitive(t *testing.T) {
	ctx := context.Background()
	dbc := openTestDB(ctx, t)
	seedJiraTask(ctx, t, dbc, "10042", "CDT-123")

	d := restartUnknownDispatcher(dbc, &countingSource{cell: model.SourceItem{ID: "10042"}})

	res, err := d.ForceRestart(ctx, "cdt-123")
	if err != nil {
		t.Fatalf("ForceRestart(lowercase key): %v", err)
	}
	if res.CellID != "10042" {
		t.Errorf("resolved cell id = %q, want 10042", res.CellID)
	}
}

// TestForceRestart_AcceptsGitHubHashRef covers the GitHub shape, where the number
// carries a '#' the item id does not. It also pins the bare-id form, which must
// keep working unchanged.
func TestForceRestart_AcceptsGitHubHashRef(t *testing.T) {
	ctx := context.Background()
	dbc := openTestDB(ctx, t)
	seedBoundTask(ctx, t, dbc, "jira", "1953") // seeds number "#1953"

	for _, ref := range []string{"#1953", "1953"} {
		d := restartUnknownDispatcher(dbc, &countingSource{cell: model.SourceItem{ID: "1953"}})
		res, err := d.ForceRestart(ctx, ref)
		if err != nil {
			t.Fatalf("ForceRestart(%q): %v", ref, err)
		}
		if res.CellID != "1953" {
			t.Errorf("ForceRestart(%q) resolved to %q, want 1953", ref, res.CellID)
		}
	}
}

// TestForceRestart_AmbiguousRefIsRejected keeps #377's fail-closed promise intact
// for the new input: a reference that names two different items in two sources
// must not be guessed at, because guessing wrong cancels an unrelated healthy run.
func TestForceRestart_AmbiguousRefIsRejected(t *testing.T) {
	ctx := context.Background()
	dbc := openTestDB(ctx, t)

	// Same human reference, two sources, two different items.
	for _, s := range []struct{ source, itemID string }{{"jira", "10042"}, {"github", "77"}} {
		task := &model.InternalTask{Title: "dup", State: model.TaskStateRunning}
		binding := &model.SourceBinding{SourceID: s.source, SourceItemID: s.itemID, SourceItemNumber: "DUP-1"}
		if err := dbc.CreateTaskWithBinding(ctx, task, binding); err != nil {
			t.Fatalf("seed %s: %v", s.source, err)
		}
	}

	fake := &countingSource{}
	d := restartUnknownDispatcher(dbc, fake)

	_, err := d.ForceRestart(ctx, "DUP-1")
	if err == nil {
		t.Fatal("an ambiguous reference must fail, got nil")
	}
	if !errors.Is(err, ErrAmbiguousRef) {
		t.Fatalf("error = %v, want it to wrap ErrAmbiguousRef", err)
	}
	if !strings.Contains(err.Error(), "jira:10042") || !strings.Contains(err.Error(), "github:77") {
		t.Errorf("error %q should name both candidates so the user can pick", err)
	}
	if len(fake.polls) != 0 || len(fake.statesSet) != 0 || fake.removeCalls != 0 {
		t.Errorf("ambiguous reference must touch nothing: polls=%v states=%v removes=%d",
			fake.polls, fake.statesSet, fake.removeCalls)
	}
}

// TestForceRestart_CellIDWinsOverNumber: when an id is simultaneously a valid cell
// id for one item and the number of another, the exact cell id must win — it is
// the unambiguous form, and resolution must never redirect it elsewhere.
func TestForceRestart_CellIDWinsOverNumber(t *testing.T) {
	ctx := context.Background()
	dbc := openTestDB(ctx, t)

	// Item A's cell id is "500". Item B's human number is also "500".
	seedJiraTask(ctx, t, dbc, "500", "CDT-1")
	seedJiraTask(ctx, t, dbc, "999", "500")

	d := restartUnknownDispatcher(dbc, &countingSource{cell: model.SourceItem{ID: "500"}})

	res, err := d.ForceRestart(ctx, "500")
	if err != nil {
		t.Fatalf("ForceRestart: %v", err)
	}
	if res.CellID != "500" {
		t.Errorf("resolved to %q, want the exact cell id 500 — an exact id must not be re-resolved as someone else's number", res.CellID)
	}
}

// TestForceRestart_UnknownRefStillFailsClosed: reference resolution must not
// become a way to smuggle an unresolvable id past the #377 guard.
func TestForceRestart_UnknownRefStillFailsClosed(t *testing.T) {
	ctx := context.Background()
	dbc := openTestDB(ctx, t)
	seedJiraTask(ctx, t, dbc, "10042", "CDT-123")

	fake := &countingSource{}
	d := restartUnknownDispatcher(dbc, fake)

	_, err := d.ForceRestart(ctx, "CDT-999")
	if !errors.Is(err, ErrUnknownCell) {
		t.Fatalf("ForceRestart(unknown key) = %v, want ErrUnknownCell", err)
	}
	if len(fake.polls) != 0 || len(fake.statesSet) != 0 {
		t.Errorf("unknown reference must touch nothing: polls=%v states=%v", fake.polls, fake.statesSet)
	}
}

// TestForceRestart_TaskIDErrorSuggestsHumanRef keeps the #377 hint useful: telling
// someone to retry with an opaque Jira item id is not much better than "unknown",
// so the suggestion names the key.
func TestForceRestart_TaskIDErrorSuggestsHumanRef(t *testing.T) {
	ctx := context.Background()
	dbc := openTestDB(ctx, t)
	taskID := seedJiraTask(ctx, t, dbc, "10042", "CDT-123")

	d := restartUnknownDispatcher(dbc, &countingSource{})

	_, err := d.ForceRestart(ctx, taskID)
	if !errors.Is(err, ErrUnknownCell) {
		t.Fatalf("ForceRestart(task id) = %v, want ErrUnknownCell", err)
	}
	if !strings.Contains(err.Error(), "CDT-123") {
		t.Errorf("error %q should suggest the key CDT-123, not just the opaque item id", err)
	}
}
