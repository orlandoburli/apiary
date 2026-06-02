package script_test

import (
	"context"
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/runner"
	_ "github.com/orlandoburli/apiary/internal/runner/script" // register adapter
)

func newRunner(t *testing.T, command string) runner.Adapter {
	t.Helper()
	r, ok := runner.New("script")
	if !ok {
		t.Fatal("script runner not registered")
	}
	if err := r.Configure(map[string]any{"command": command}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	return r
}

func TestRun_Success(t *testing.T) {
	r := newRunner(t, "echo hello")
	result, err := r.Run(context.Background(), model.RunRequest{
		Cell:     model.Cell{ID: "c1", Title: "Test task"},
		WorkerID: "w1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected Success=true, got false")
	}
	if !strings.Contains(result.Output, "hello") {
		t.Errorf("output %q should contain 'hello'", result.Output)
	}
}

func TestRun_CellEnvInjected(t *testing.T) {
	r := newRunner(t, "echo $APIARY_CELL_TITLE")
	result, _ := r.Run(context.Background(), model.RunRequest{
		Cell:     model.Cell{ID: "c1", Title: "My Task Title"},
		WorkerID: "w1",
	})
	if !strings.Contains(result.Output, "My Task Title") {
		t.Errorf("expected APIARY_CELL_TITLE in output, got: %q", result.Output)
	}
}

func TestRun_Failure(t *testing.T) {
	r := newRunner(t, "exit 1")
	result, _ := r.Run(context.Background(), model.RunRequest{WorkerID: "w1"})
	if result.Success {
		t.Error("expected Success=false for non-zero exit")
	}
	if result.Error == nil {
		t.Error("expected non-nil Error for non-zero exit")
	}
}

func TestRun_ExtraEnvInjected(t *testing.T) {
	r := newRunner(t, "echo $MY_VAR")
	result, _ := r.Run(context.Background(), model.RunRequest{
		WorkerID: "w1",
		Env:      map[string]string{"MY_VAR": "injected"},
	})
	if !strings.Contains(result.Output, "injected") {
		t.Errorf("expected MY_VAR in output, got: %q", result.Output)
	}
}

func TestRun_LogsStreamed(t *testing.T) {
	r := newRunner(t, "printf 'line1\nline2\n'")
	result, _ := r.Run(context.Background(), model.RunRequest{WorkerID: "w1"})
	if len(result.Logs) < 2 {
		t.Errorf("expected at least 2 log entries, got %d", len(result.Logs))
	}
}

func TestRun_DurationSet(t *testing.T) {
	r := newRunner(t, "echo ok")
	result, _ := r.Run(context.Background(), model.RunRequest{WorkerID: "w1"})
	if result.Duration == 0 {
		t.Error("expected non-zero Duration")
	}
}

func TestConfigure_MissingCommand(t *testing.T) {
	r, _ := runner.New("script")
	if err := r.Configure(map[string]any{}); err == nil {
		t.Error("expected error for missing command, got nil")
	}
}
