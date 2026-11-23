package dashboard

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

// A waiting row carries two ids: the internal task ulid and the source cell.
// The workflow monitor looks instances up by cell, so the row's actions must
// resolve to the cell. Handing it the ulid found nothing and fell back to a
// logs view, which is why enter looked dead on a waiting row.
func TestWaitingRowActionsUseTheCellID(t *testing.T) {
	ctx := context.Background()
	c, err := db.New(ctx, filepath.Join(t.TempDir(), "waiting-actions.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	const (
		internalID = "01a2021a7d5c9c255e4ad0aa67807e4f"
		cellID     = "8073"
		taskURL    = "https://github.com/orlandoburli/project-erp/issues/8073"
	)
	if err := c.InternalTasks().CreateTask(ctx, &model.InternalTask{
		ID: internalID, Title: "Tela do gestor matcon", State: model.TaskStateRunning,
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := c.SourceBindings().CreateBinding(ctx, &model.SourceBinding{
		ID: "b1", TaskID: internalID, SourceID: "github", SourceItemID: cellID,
		SourceItemURL: taskURL, SourceItemNumber: "#" + cellID,
	}); err != nil {
		t.Fatalf("create binding: %v", err)
	}
	now := time.Now().Add(-time.Hour)
	if err := c.CreateWorkflowInstance(ctx, &db.WorkflowInstance{
		ID: "wf1", WorkflowID: "implementation", TaskID: internalID, CellID: cellID,
		State: "blocked", BlockedReason: "ci", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create instance: %v", err)
	}

	a := newTestApp(140, 40)
	a.dbConn = c
	a.model.activeTab = tabIndex(a.model, "Running")

	upd, _ := a.Update(a.fetchRunning()())
	a = upd.(*App)
	if len(a.model.runningTab.Waiting) != 1 {
		t.Fatalf("got %d waiting rows, want 1", len(a.model.runningTab.Waiting))
	}
	a.model.runningTab.SelectedIdx = 0 // no running agents: the waiting row

	if id, ok := a.selectedRunningTaskID(); !ok || id != cellID {
		t.Fatalf("selectedRunningTaskID = %q (%v), want the cell id %q", id, ok, cellID)
	}

	// Enter is only useful if the id it passes actually finds the instance.
	if items := listWorkflowInstanceItems(ctx, c, cellID); len(items) == 0 {
		t.Fatal("enter would fall back to logs: no instances resolve for the cell id")
	}
	// And it must be the cell, not the ulid: the two id spaces are distinct.
	if items := listWorkflowInstanceItems(ctx, c, internalID); len(items) != 0 {
		t.Errorf("the internal id resolved %d instances, so the two id spaces would not be distinguishable", len(items))
	}

	// `o` opens the binding's link. A waiting workflow has no execution row to
	// carry it, so it comes from the binding.
	if u, ok := a.focusedTaskURL(); !ok || u != taskURL {
		t.Errorf("focusedTaskURL = %q (%v), want %q", u, ok, taskURL)
	}
}

// A waiting row with no source binding has no link; `o` must do nothing rather
// than open something else.
func TestWaitingRowWithoutABindingHasNoURL(t *testing.T) {
	ctx := context.Background()
	c, err := db.New(ctx, filepath.Join(t.TempDir(), "no-binding.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	now := time.Now().Add(-time.Hour)
	if err := c.CreateWorkflowInstance(ctx, &db.WorkflowInstance{
		ID: "wf1", WorkflowID: "implementation", TaskID: "internal-1", CellID: "9001",
		State: "blocked", BlockedReason: "approval", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create instance: %v", err)
	}

	insts, err := c.ListNonTerminalInstances(ctx, 10)
	if err != nil {
		t.Fatalf("ListNonTerminalInstances: %v", err)
	}
	if len(insts) != 1 || insts[0].TaskURL != "" {
		t.Fatalf("instances = %+v, want one with an empty TaskURL", insts)
	}

	a := newTestApp(140, 40)
	a.dbConn = c
	a.model.activeTab = tabIndex(a.model, "Running")
	upd, _ := a.Update(a.fetchRunning()())
	a = upd.(*App)
	a.model.runningTab.SelectedIdx = 0

	if u, ok := a.focusedTaskURL(); ok || u != "" {
		t.Errorf("focusedTaskURL = %q (%v), want no URL", u, ok)
	}
}

// The task's link comes from its binding even though the workflow has no open
// execution — the state a parked workflow is always in.
func TestParkedWorkflowStillCarriesItsTaskLink(t *testing.T) {
	ctx := context.Background()
	c, err := db.New(ctx, filepath.Join(t.TempDir(), "parked.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	const internalID = "internal-1"
	if err := c.InternalTasks().CreateTask(ctx, &model.InternalTask{
		ID: internalID, Title: "parked", State: model.TaskStateRunning,
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := c.SourceBindings().CreateBinding(ctx, &model.SourceBinding{
		ID: "b1", TaskID: internalID, SourceID: "github", SourceItemID: "1",
		SourceItemURL: "https://example.test/1",
	}); err != nil {
		t.Fatalf("create binding: %v", err)
	}
	now := time.Now().Add(-time.Hour)
	if err := c.CreateWorkflowInstance(ctx, &db.WorkflowInstance{
		ID: "wf1", WorkflowID: "implementation", TaskID: internalID, CellID: "1",
		State: "blocked", BlockedReason: "approval", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create instance: %v", err)
	}

	insts, err := c.ListNonTerminalInstances(ctx, 10)
	if err != nil {
		t.Fatalf("ListNonTerminalInstances: %v", err)
	}
	if len(insts) != 1 {
		t.Fatalf("got %d instances, want 1", len(insts))
	}
	if insts[0].TaskURL != "https://example.test/1" {
		t.Errorf("TaskURL = %q, want the binding's link", insts[0].TaskURL)
	}
}
