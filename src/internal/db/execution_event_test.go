package db

import (
	"context"
	"testing"
	"time"
)

func TestExecutionEventsPersistFilterRedactAndStream(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	c.SetEventSensitiveFields([]string{"customer_key"})
	live, cancel := c.SubscribeExecutionEvents(1)
	defer cancel()

	event := &ExecutionEvent{Type: "runner.started", TaskID: "task-1", WorkflowInstanceID: "inst-1", Metadata: map[string]any{
		"runner": "codex", "access_token": "plain", "customer_key": "value", "nested": map[string]any{"value": "Bearer abc"},
	}}
	if err := c.RecordExecutionEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	if event.ID == 0 || event.SchemaVersion != ExecutionEventSchemaVersion {
		t.Fatalf("unstable envelope: %+v", event)
	}
	if event.Metadata["access_token"] != "[REDACTED]" || event.Metadata["customer_key"] != "[REDACTED]" {
		t.Fatalf("metadata not redacted: %#v", event.Metadata)
	}
	select {
	case got := <-live:
		if got.ID != event.ID || got.Metadata["access_token"] != "[REDACTED]" {
			t.Fatalf("bad live event: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("live event not delivered")
	}
	events, err := c.ListExecutionEvents(ctx, ExecutionEventFilter{TaskID: "task-1", AfterID: event.ID - 1})
	if err != nil || len(events) != 1 || events[0].Type != "runner.started" {
		t.Fatalf("query: %+v, %v", events, err)
	}
	if nested := events[0].Metadata["nested"].(map[string]any); nested["value"] != "[REDACTED]" {
		t.Fatalf("nested secret leaked: %#v", nested)
	}
}

func TestExecutionEventPruning(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	old := &ExecutionEvent{Type: "task.discovered", Timestamp: time.Now().Add(-48 * time.Hour)}
	current := &ExecutionEvent{Type: "task.bound"}
	if err := c.RecordExecutionEvent(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := c.RecordExecutionEvent(ctx, current); err != nil {
		t.Fatal(err)
	}
	n, err := c.PruneExecutionEventsBefore(ctx, time.Now().Add(-24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("prune = %d, %v", n, err)
	}
	events, _ := c.ListExecutionEvents(ctx, ExecutionEventFilter{})
	if len(events) != 1 || events[0].ID != current.ID {
		t.Fatalf("events after prune: %+v", events)
	}
}

func TestWorkflowAndStepLifecycleEvents(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	inst := &WorkflowInstance{ID: "inst", WorkflowID: "build", TaskID: "task", State: InstanceStateRunning}
	if err := c.CreateWorkflowInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}
	step := &StepRun{ID: "run", WorkflowInstanceID: inst.ID, StepID: "test", State: StepStateRunning}
	if err := c.CreateStepRun(ctx, step); err != nil {
		t.Fatal(err)
	}
	step.State = StepStatePassed
	if err := c.UpdateStepRun(ctx, step); err != nil {
		t.Fatal(err)
	}
	if err := c.UpdateWorkflowInstanceState(ctx, inst.ID, InstanceStateDone); err != nil {
		t.Fatal(err)
	}
	events, _ := c.ListExecutionEvents(ctx, ExecutionEventFilter{TaskID: "task"})
	want := []string{"workflow.started", "step.started", "step.completed", "workflow.completed"}
	if len(events) != len(want) {
		t.Fatalf("events: %+v", events)
	}
	for i := range want {
		if events[i].Type != want[i] {
			t.Fatalf("event %d = %s, want %s", i, events[i].Type, want[i])
		}
	}
}
