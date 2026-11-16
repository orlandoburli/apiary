package db

import (
	"context"
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/model"
)

func TestTaskPageIndexExistingDatabase(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	task := &model.InternalTask{Title: "Existing task"}
	if err := c.InternalTasks().CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	// Simulate a database created before the pagination index existed.
	if _, err := c.db.ExecContext(ctx, "DROP INDEX idx_internal_tasks_created_id"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := InitSchema(ctx, c.db); err != nil {
			t.Fatal(err)
		}
	}
	got, err := c.InternalTasks().GetTask(ctx, task.ID)
	if err != nil || got == nil || got.Title != task.Title {
		t.Fatalf("existing task changed: task=%+v err=%v", got, err)
	}
	assertTaskPageIndex(t, c)
}

func TestTaskPageIndexNewDatabase(t *testing.T) {
	assertTaskPageIndex(t, newTestClient(t))
}

func assertTaskPageIndex(t *testing.T, c *Client) {
	t.Helper()
	// Exercise both the initial page and the keyset predicate used for older pages.
	for _, predicate := range []string{"1=1", "(t.created_at < '2026-11-15' OR (t.created_at = '2026-11-15' AND t.id < 'cursor'))"} {
		rows, err := c.db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN SELECT t.* FROM internal_tasks t WHERE "+predicate+" ORDER BY t.created_at DESC, t.id DESC LIMIT 100")
		if err != nil {
			t.Fatal(err)
		}
		var plan strings.Builder
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			plan.WriteString(detail + "\n")
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(plan.String(), "idx_internal_tasks_created_id") || strings.Contains(plan.String(), "TEMP B-TREE") {
			t.Fatalf("pagination should use the index without a temporary sort:\n%s", plan.String())
		}
	}
}
