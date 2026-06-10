package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
	runnerpkg "github.com/orlandoburli/apiary/internal/runner"
	"github.com/orlandoburli/apiary/internal/workflow"
)

// scriptedRunner returns a fixed result and records its invocation count, so a
// test can assert which runner in a failover chain actually ran.
type scriptedRunner struct {
	result model.RunResult
	calls  int
}

func (s *scriptedRunner) ID() string                       { return "scripted" }
func (s *scriptedRunner) Configure(_ map[string]any) error { return nil }
func (s *scriptedRunner) Run(_ context.Context, _ model.RunRequest) (model.RunResult, error) {
	s.calls++
	return s.result, nil
}

func newFailoverDispatcher(t *testing.T, primary, fallback runnerpkg.Runner, fbType, fbModel string) (*Dispatcher, *db.Client) {
	t.Helper()
	dbc, err := db.New(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })
	d := &Dispatcher{
		cfg:             &config.Config{},
		db:              dbc,
		runners:         map[string]runnerpkg.Runner{"agent-engineer": primary},
		agentRunner:     map[string]string{"engineer": "claude"},
		agentFallbacks:  map[string][]runnerCandidate{"engineer": {{adapter: fallback, runnerType: fbType, model: fbModel}}},
		rateLimitPaused: map[string]time.Time{},
	}
	return d, dbc
}

func stepReq(instance, cellID string) workflow.StepRequest {
	return workflow.StepRequest{
		InstanceID: instance,
		Cell:       model.SourceItem{ID: cellID, Title: "T", Number: "#1"},
		Step:       config.StepConfig{ID: "implement", Agent: "engineer"},
		Model:      "claude-sonnet-4-6",
	}
}

// A rate-limited primary fails over to the fallback runner, the fallback's
// result is returned, and the primary's runner type is paused.
func TestWfStepExecutor_RateLimitFailover(t *testing.T) {
	ctx := context.Background()
	resets := time.Now().Add(2 * time.Hour)
	primary := &scriptedRunner{result: model.RunResult{Success: true, RateLimited: true, RateLimitResetsAt: resets, Output: "limit"}}
	fallback := &scriptedRunner{result: model.RunResult{Success: true, Output: "did the work"}}
	d, _ := newFailoverDispatcher(t, primary, fallback, "opencode", "opencode-go/deepseek-v4-pro")
	x := &wfStepExecutor{d: d}

	res := x.ExecuteStep(ctx, stepReq("wf_1", "c1"))

	if primary.calls != 1 {
		t.Errorf("primary calls = %d, want 1", primary.calls)
	}
	if fallback.calls != 1 {
		t.Errorf("fallback calls = %d, want 1 (failover)", fallback.calls)
	}
	if !res.Success || res.Output != "did the work" {
		t.Errorf("expected fallback result, got success=%v output=%q", res.Success, res.Output)
	}
	if got := d.runnerPausedUntil("claude"); !got.Equal(resets) {
		t.Errorf("claude pause = %v, want %v", got, resets)
	}
}

// When the primary's runner type is already paused, the primary is skipped and
// the fallback runs directly — no wasted rate-limited call.
func TestWfStepExecutor_SkipsPausedPrimary(t *testing.T) {
	ctx := context.Background()
	primary := &scriptedRunner{result: model.RunResult{Success: true}}
	fallback := &scriptedRunner{result: model.RunResult{Success: true, Output: "fallback ran"}}
	d, _ := newFailoverDispatcher(t, primary, fallback, "opencode", "m")
	d.pauseRunner("claude", time.Now().Add(time.Hour))
	x := &wfStepExecutor{d: d}

	res := x.ExecuteStep(ctx, stepReq("wf_1", "c1"))

	if primary.calls != 0 {
		t.Errorf("primary calls = %d, want 0 (paused, skipped)", primary.calls)
	}
	if fallback.calls != 1 {
		t.Errorf("fallback calls = %d, want 1", fallback.calls)
	}
	if res.Output != "fallback ran" {
		t.Errorf("output = %q, want fallback", res.Output)
	}
}

func TestPauseRunner(t *testing.T) {
	d := &Dispatcher{}
	if !d.runnerPausedUntil("claude").IsZero() {
		t.Error("unset runner should not be paused")
	}
	// nil-map safe + zero reset defaults to ~5m out.
	d.pauseRunner("claude", time.Time{})
	if got := d.runnerPausedUntil("claude"); got.Before(time.Now().Add(4 * time.Minute)) {
		t.Errorf("zero reset should default ~5m out, got %v", got)
	}
	// Only extends, never shortens.
	far := time.Now().Add(2 * time.Hour)
	d.pauseRunner("claude", far)
	d.pauseRunner("claude", time.Now().Add(time.Minute))
	if got := d.runnerPausedUntil("claude"); !got.Equal(far) {
		t.Errorf("pause shortened: got %v, want %v", got, far)
	}
}
