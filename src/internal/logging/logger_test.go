package logging

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/orlandoburli/apiary/internal/db"
)

// At INFO threshold, DEBUG task logs must still reach the DB so the dashboard's
// task detail view shows the full agent stream, while DEBUG service logs are
// dropped as before.
func TestLog_TaskDebugPersistsBelowThreshold(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	dbClient, err := db.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	defer dbClient.Close()

	logger, err := New(t.TempDir(), dbClient, LevelInfo)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer logger.Close()

	// DEBUG is below the INFO threshold.
	logger.TaskDebug(ctx, "task-1", "verbose agent stream line")
	logger.Debug(ctx, "service debug line", "dispatcher")

	taskLogs, err := dbClient.GetTaskLogs(ctx, "task-1", 100)
	if err != nil {
		t.Fatalf("GetTaskLogs: %v", err)
	}
	if len(taskLogs) != 1 {
		t.Fatalf("expected 1 task log persisted below threshold, got %d", len(taskLogs))
	}
	if taskLogs[0].Level != string(LevelDebug) {
		t.Errorf("task log level = %q, want %q", taskLogs[0].Level, LevelDebug)
	}
	if taskLogs[0].Message != "verbose agent stream line" {
		t.Errorf("task log message = %q", taskLogs[0].Message)
	}

	// Service-level DEBUG logs remain gated by the threshold.
	svcLogs, err := dbClient.GetRecentLogs(ctx, 100)
	if err != nil {
		t.Fatalf("GetRecentLogs: %v", err)
	}
	for _, l := range svcLogs {
		if l.Message == "service debug line" {
			t.Errorf("service DEBUG log should have been dropped below threshold")
		}
	}
}
