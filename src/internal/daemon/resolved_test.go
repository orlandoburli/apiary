package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

// fakeResolver is a source.Adapter that also implements source.ItemResolver,
// recording what it was asked and returning a canned answer.
type fakeResolver struct {
	resolved []string
	err      error
	asked    [][]string
}

func (f *fakeResolver) ID() string                                    { return "prod-alerts" }
func (f *fakeResolver) Connect(context.Context, map[string]any) error { return nil }
func (f *fakeResolver) Poll(context.Context, time.Time) ([]model.SourceItem, error) {
	return nil, nil
}
func (f *fakeResolver) Acknowledge(context.Context, model.SourceItem, model.AckAction) error {
	return nil
}
func (f *fakeResolver) WriteResult(context.Context, model.SourceItem, model.RunResult) error {
	return nil
}
func (f *fakeResolver) ResolvedItems(_ context.Context, ids []string) ([]string, error) {
	f.asked = append(f.asked, append([]string(nil), ids...))
	return f.resolved, f.err
}

func instanceState(ctx context.Context, t *testing.T, dbc *db.Client, id string) string {
	t.Helper()
	inst, err := dbc.GetWorkflowInstance(ctx, id)
	if err != nil || inst == nil {
		t.Fatalf("get instance %s: inst=%v err=%v", id, inst, err)
	}
	return inst.State
}

func srcCfg(interrupt bool) config.SourceConfig {
	return config.SourceConfig{ID: "prod-alerts", Type: "prometheus", InterruptOnResolve: interrupt}
}

// The default is unchanged: a resolved alert never interrupts a running run.
func TestCheckResolved_OffByDefault(t *testing.T) {
	ctx := context.Background()
	dbc := openTestDB(ctx, t)
	_, instID := seedBoundTask(ctx, t, dbc, "prod-alerts", "fp:2026-01-01T00:00:00Z")

	adapter := &fakeResolver{resolved: []string{"fp:2026-01-01T00:00:00Z"}}
	d := &Dispatcher{db: dbc, cfg: &config.Config{}}
	d.checkResolved(ctx, srcCfg(false), adapter)

	if len(adapter.asked) != 0 {
		t.Errorf("resolution was queried despite interrupt_on_resolve being off")
	}
	if got := instanceState(ctx, t, dbc, instID); got != db.InstanceStateRunning {
		t.Fatalf("instance state = %q, want %q", got, db.InstanceStateRunning)
	}
}

// With the opt-in on, a resolved alert stops its running instance.
func TestCheckResolved_StopsInstance(t *testing.T) {
	ctx := context.Background()
	dbc := openTestDB(ctx, t)
	itemID := "fp:2026-01-01T00:00:00Z"
	_, instID := seedBoundTask(ctx, t, dbc, "prod-alerts", itemID)

	adapter := &fakeResolver{resolved: []string{itemID}}
	d := &Dispatcher{db: dbc, cfg: &config.Config{}}
	d.checkResolved(ctx, srcCfg(true), adapter)

	if len(adapter.asked) != 1 || len(adapter.asked[0]) != 1 || adapter.asked[0][0] != itemID {
		t.Fatalf("adapter asked %v, want [[%s]]", adapter.asked, itemID)
	}
	if got := instanceState(ctx, t, dbc, instID); got != db.InstanceStateBlocked {
		t.Fatalf("instance state = %q, want %q", got, db.InstanceStateBlocked)
	}
}

// An alert still firing leaves its instance alone.
func TestCheckResolved_LeavesUnresolvedRunning(t *testing.T) {
	ctx := context.Background()
	dbc := openTestDB(ctx, t)
	_, instID := seedBoundTask(ctx, t, dbc, "prod-alerts", "fp:2026-01-01T00:00:00Z")

	adapter := &fakeResolver{} // nothing resolved
	d := &Dispatcher{db: dbc, cfg: &config.Config{}}
	d.checkResolved(ctx, srcCfg(true), adapter)

	if got := instanceState(ctx, t, dbc, instID); got != db.InstanceStateRunning {
		t.Fatalf("instance state = %q, want %q", got, db.InstanceStateRunning)
	}
}

// A source that cannot answer must not take anything down.
func TestCheckResolved_FailsClosedOnError(t *testing.T) {
	ctx := context.Background()
	dbc := openTestDB(ctx, t)
	itemID := "fp:2026-01-01T00:00:00Z"
	_, instID := seedBoundTask(ctx, t, dbc, "prod-alerts", itemID)

	adapter := &fakeResolver{resolved: []string{itemID}, err: errors.New("alertmanager unreachable")}
	d := &Dispatcher{db: dbc, cfg: &config.Config{}}
	d.checkResolved(ctx, srcCfg(true), adapter)

	if got := instanceState(ctx, t, dbc, instID); got != db.InstanceStateRunning {
		t.Fatalf("instance state = %q, want %q — an error must never read as resolved", got, db.InstanceStateRunning)
	}
}

// Instances belonging to another source must not be touched, and must not even
// be offered to this source's resolver.
func TestCheckResolved_IgnoresOtherSources(t *testing.T) {
	ctx := context.Background()
	dbc := openTestDB(ctx, t)
	_, otherInst := seedBoundTask(ctx, t, dbc, "github", "1956")

	adapter := &fakeResolver{resolved: []string{"1956"}}
	d := &Dispatcher{db: dbc, cfg: &config.Config{}}
	d.checkResolved(ctx, srcCfg(true), adapter)

	if len(adapter.asked) != 0 {
		t.Errorf("asked about another source's items: %v", adapter.asked)
	}
	if got := instanceState(ctx, t, dbc, otherInst); got != db.InstanceStateRunning {
		t.Fatalf("other source's instance state = %q, want %q", got, db.InstanceStateRunning)
	}
}

// A parked instance is as pointless to keep alive as a running one.
func TestCheckResolved_StopsParkedInstance(t *testing.T) {
	for _, state := range []string{db.InstanceStateBlocked, db.InstanceStateBlocked} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			dbc := openTestDB(ctx, t)
			itemID := "fp:2026-01-01T00:00:00Z"
			_, instID := seedBoundTask(ctx, t, dbc, "prod-alerts", itemID)
			if err := dbc.UpdateWorkflowInstanceState(ctx, instID, state, ""); err != nil {
				t.Fatalf("park instance: %v", err)
			}

			adapter := &fakeResolver{resolved: []string{itemID}}
			d := &Dispatcher{db: dbc, cfg: &config.Config{}}
			d.checkResolved(ctx, srcCfg(true), adapter)

			if got := instanceState(ctx, t, dbc, instID); got != db.InstanceStateBlocked {
				t.Fatalf("instance state = %q, want %q", got, db.InstanceStateBlocked)
			}
		})
	}
}

// A finished instance is terminal and must stay that way.
func TestCheckResolved_LeavesTerminalInstances(t *testing.T) {
	ctx := context.Background()
	dbc := openTestDB(ctx, t)
	itemID := "fp:2026-01-01T00:00:00Z"
	_, instID := seedBoundTask(ctx, t, dbc, "prod-alerts", itemID)
	if err := dbc.UpdateWorkflowInstanceState(ctx, instID, db.InstanceStateDone, ""); err != nil {
		t.Fatalf("finish instance: %v", err)
	}

	adapter := &fakeResolver{resolved: []string{itemID}}
	d := &Dispatcher{db: dbc, cfg: &config.Config{}}
	d.checkResolved(ctx, srcCfg(true), adapter)

	if len(adapter.asked) != 0 {
		t.Errorf("asked about a terminal instance's item: %v", adapter.asked)
	}
	if got := instanceState(ctx, t, dbc, instID); got != db.InstanceStateDone {
		t.Fatalf("instance state = %q, want %q", got, db.InstanceStateDone)
	}
}

// Nothing in flight → no API call.
func TestCheckResolved_NoInFlightWork(t *testing.T) {
	ctx := context.Background()
	dbc := openTestDB(ctx, t)

	adapter := &fakeResolver{}
	d := &Dispatcher{db: dbc, cfg: &config.Config{}}
	d.checkResolved(ctx, srcCfg(true), adapter)

	if len(adapter.asked) != 0 {
		t.Errorf("queried the source with no in-flight instances: %v", adapter.asked)
	}
}
