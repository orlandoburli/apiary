package db

import (
	"context"
	"fmt"
	"testing"
)

// GetTaskLogs returns the newest page; GetTaskLogsBefore walks older pages via an
// id cursor with no overlap, draining cleanly to the start.
func TestGetTaskLogs_CursorPagination(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	for i := 1; i <= 10; i++ {
		if err := c.WriteTaskLog(ctx, "42", "info", fmt.Sprintf("line-%d", i)); err != nil {
			t.Fatalf("write log: %v", err)
		}
	}

	// Newest page of 3 → chronological line-8, line-9, line-10.
	page, err := c.GetTaskLogs(ctx, "42", 3)
	if err != nil {
		t.Fatalf("GetTaskLogs: %v", err)
	}
	if len(page) != 3 || page[0].Message != "line-8" || page[2].Message != "line-10" {
		t.Fatalf("tail page wrong: %+v", page)
	}

	// Walk older pages via the cursor, reassembling the full ordered history.
	seen := messages(page)
	for len(page) > 0 {
		older, err := c.GetTaskLogsBefore(ctx, "42", page[0].ID, 3)
		if err != nil {
			t.Fatalf("GetTaskLogsBefore: %v", err)
		}
		if len(older) > 0 && older[len(older)-1].ID >= page[0].ID {
			t.Fatalf("older page overlaps the cursor: older=%+v cursor=%d", older, page[0].ID)
		}
		seen = append(messages(older), seen...)
		page = older
	}

	want := make([]string, 10)
	for i := range want {
		want[i] = fmt.Sprintf("line-%d", i+1)
	}
	if fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Errorf("reassembled history = %v, want %v", seen, want)
	}
}

// GetTaskLogsAfter returns only the lines newer than the cursor, oldest-first, so
// the logs view can live-tail by passing its newest loaded row id.
func TestGetTaskLogsAfter_Cursor(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	for i := 1; i <= 6; i++ {
		if err := c.WriteTaskLog(ctx, "7", "info", fmt.Sprintf("line-%d", i)); err != nil {
			t.Fatalf("write log: %v", err)
		}
	}

	// Load a tail of 3 (line-4..line-6), then tail forward from its newest id.
	tail, err := c.GetTaskLogs(ctx, "7", 3)
	if err != nil {
		t.Fatalf("GetTaskLogs: %v", err)
	}
	cursor := tail[len(tail)-1].ID

	// Nothing newer yet.
	if more, err := c.GetTaskLogsAfter(ctx, "7", cursor, 100); err != nil {
		t.Fatalf("GetTaskLogsAfter: %v", err)
	} else if len(more) != 0 {
		t.Fatalf("expected no new lines, got %+v", more)
	}

	// Two more lines arrive; only those come back, oldest-first.
	for i := 7; i <= 8; i++ {
		if err := c.WriteTaskLog(ctx, "7", "info", fmt.Sprintf("line-%d", i)); err != nil {
			t.Fatalf("write log: %v", err)
		}
	}
	more, err := c.GetTaskLogsAfter(ctx, "7", cursor, 100)
	if err != nil {
		t.Fatalf("GetTaskLogsAfter: %v", err)
	}
	if got := messages(more); fmt.Sprint(got) != fmt.Sprint([]string{"line-7", "line-8"}) {
		t.Errorf("tail-after = %v, want [line-7 line-8]", got)
	}
	if more[0].ID <= cursor {
		t.Errorf("returned a line at/under the cursor: id=%d cursor=%d", more[0].ID, cursor)
	}
}

func messages(rows []TaskLogLine) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Message)
	}
	return out
}
