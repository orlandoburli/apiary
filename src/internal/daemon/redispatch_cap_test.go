package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/router"
)

// dropCappedMatches drops a (task, workflow) that has hit max_attempts
// consecutive failures, keeps one still under the cap, and respects disabling.
func TestDropCappedMatches(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	for i := 0; i < 3; i++ { // capped workflow: 3 consecutive failures
		_ = dbc.CreateWorkflowInstance(ctx, &db.WorkflowInstance{
			ID: fmt.Sprintf("f%d", i), WorkflowID: "impl", CellID: "1", TaskID: "T1", State: db.InstanceStateFailed,
		})
	}
	_ = dbc.CreateWorkflowInstance(ctx, &db.WorkflowInstance{ // under cap
		ID: "h0", WorkflowID: "hatch", CellID: "1", TaskID: "T1", State: db.InstanceStateFailed,
	})

	d := &Dispatcher{cfg: &config.Config{Settings: config.Settings{MaxAttempts: 3}}, db: dbc}
	task := model.InternalTask{ID: "T1"}
	matches := []router.Match{
		{Route: config.RouteConfig{ID: "impl"}},
		{Route: config.RouteConfig{ID: "hatch"}},
	}

	out, _ := d.dropCappedMatches(ctx, task, matches)
	if len(out) != 1 || out[0].Route.ID != "hatch" {
		t.Fatalf("expected only 'hatch' kept, got %+v", out)
	}

	// max_attempts <= 0 disables the cap entirely.
	d.cfg.Settings.MaxAttempts = 0
	if got, _ := d.dropCappedMatches(ctx, task, matches); len(got) != 2 {
		t.Errorf("disabled cap should keep all, got %d", len(got))
	}
}
