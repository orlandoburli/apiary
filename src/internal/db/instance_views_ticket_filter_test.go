package db

import (
	"context"
	"testing"
)

// TestListWorkflowInstanceViews_TicketSourceFilter covers the allow-list param
// added for issue #475 (apiary instances --tickets-only / the dashboard's
// equivalent toggle): nil keeps today's unfiltered behavior, a populated list
// restricts to those source ids, and an empty-but-non-nil list (no ticket
// sources configured) correctly returns nothing rather than everything.
func TestListWorkflowInstanceViews_TicketSourceFilter(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	mustCreate := func(id, cellID, sourceID string) {
		t.Helper()
		if err := c.CreateWorkflowInstance(ctx, &WorkflowInstance{
			ID: id, WorkflowID: "wf", CellID: cellID, SourceID: sourceID, State: InstanceStateDone,
		}); err != nil {
			t.Fatalf("create instance %s: %v", id, err)
		}
	}

	mustCreate("inst-jira", "cell-1", "jira")
	mustCreate("inst-github", "cell-2", "github")
	mustCreate("inst-routine", "cell-3", "routines")
	mustCreate("inst-nosource", "cell-4", "")

	// nil: unfiltered, unchanged behavior — every instance comes back.
	all, err := c.ListWorkflowInstanceViews(ctx, "", "", nil, 20)
	if err != nil {
		t.Fatalf("ListWorkflowInstanceViews(nil): %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("unfiltered: got %d instances, want 4", len(all))
	}

	// Populated allow-list: only matching source ids.
	ticketed, err := c.ListWorkflowInstanceViews(ctx, "", "", []string{"jira", "github"}, 20)
	if err != nil {
		t.Fatalf("ListWorkflowInstanceViews(ticket ids): %v", err)
	}
	if len(ticketed) != 2 {
		t.Fatalf("ticket-filtered: got %d instances, want 2", len(ticketed))
	}
	for _, v := range ticketed {
		if v.SourceID != "jira" && v.SourceID != "github" {
			t.Fatalf("unexpected source_id %q in ticket-filtered results", v.SourceID)
		}
	}

	// Empty-but-non-nil: no ticket-tracker sources configured, so nothing
	// qualifies — not "no filter".
	none, err := c.ListWorkflowInstanceViews(ctx, "", "", []string{}, 20)
	if err != nil {
		t.Fatalf("ListWorkflowInstanceViews(empty ids): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("empty allow-list: got %d instances, want 0", len(none))
	}
}
