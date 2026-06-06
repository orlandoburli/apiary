package model

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestInternalTaskZeroValue(t *testing.T) {
	var task InternalTask
	if task.ID != "" {
		t.Errorf("zero-value ID = %q, want empty", task.ID)
	}
	if task.ParentTaskID != "" {
		t.Errorf("zero-value ParentTaskID = %q, want empty", task.ParentTaskID)
	}
	if task.State != "" {
		t.Errorf("zero-value State = %q, want empty", task.State)
	}
	if task.Input != nil {
		t.Errorf("zero-value Input = %v, want nil", task.Input)
	}
	if task.OutstandingWorkflows != 0 {
		t.Errorf("zero-value OutstandingWorkflows = %d, want 0", task.OutstandingWorkflows)
	}
	if len(task.Metadata.Labels) != 0 {
		t.Errorf("zero-value Metadata.Labels = %v, want empty", task.Metadata.Labels)
	}
}

func TestSourceBindingZeroValue(t *testing.T) {
	var b SourceBinding
	if b.ID != "" || b.TaskID != "" || b.SourceID != "" || b.SourceItemID != "" {
		t.Errorf("zero-value SourceBinding has non-empty fields: %+v", b)
	}
}

func TestSpawnRequestZeroValue(t *testing.T) {
	var r SpawnRequest
	if r.ParentTaskID != "" || r.WorkflowID != "" || r.Title != "" {
		t.Errorf("zero-value SpawnRequest has non-empty fields: %+v", r)
	}
	if r.Input != nil {
		t.Errorf("zero-value SpawnRequest.Input = %v, want nil", r.Input)
	}
}

func TestTaskStateConstants(t *testing.T) {
	cases := map[TaskState]string{
		TaskStateRegistered:   "registered",
		TaskStateRunning:      "running",
		TaskStateApprovalWait: "approval_waiting",
		TaskStateDone:         "done",
		TaskStateFailed:       "failed",
	}
	for got, want := range cases {
		if string(got) != want {
			t.Errorf("TaskState = %q, want %q", got, want)
		}
	}
}

// TestTaskInputJSONRoundTrip verifies the structured Input payload survives a
// marshal/unmarshal cycle, which is how it is persisted in internal_tasks.input.
func TestTaskInputJSONRoundTrip(t *testing.T) {
	in := map[string]any{
		"service":  "payments",
		"severity": "critical",
		"attempt":  float64(3), // JSON numbers decode to float64
		"nested": map[string]any{
			"region": "us-east-1",
		},
	}

	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(in, out) {
		t.Errorf("round-trip mismatch:\n got = %#v\nwant = %#v", out, in)
	}
}

// TestTaskMetadataJSONRoundTrip verifies TaskMetadata serializes as stored in
// internal_tasks.metadata.
func TestTaskMetadataJSONRoundTrip(t *testing.T) {
	meta := TaskMetadata{
		Labels:   []string{"apiary", "workflow:engineer"},
		Priority: "high",
		Type:     "issue",
	}

	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out TaskMetadata
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(meta, out) {
		t.Errorf("round-trip mismatch:\n got = %#v\nwant = %#v", out, meta)
	}
}
