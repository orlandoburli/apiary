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

func messages(rows []TaskLogLine) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Message)
	}
	return out
}
