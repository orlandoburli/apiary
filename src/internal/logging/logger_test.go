package logging

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	logger, err := New(t.TempDir(), dbClient, LevelInfo, DefaultRotation())
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

// When SetPersistPrompts(false), task-log entries that begin with
// "prompt sent to agent:" must be silently dropped from the DB while all
// other entries — including the rest of the agent stream — are still written.
func TestLog_SetPersistPromptsDropsPromptEntries(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	dbClient, err := db.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	defer dbClient.Close()

	logger, err := New(t.TempDir(), dbClient, LevelDebug, DefaultRotation())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer logger.Close()

	logger.SetPersistPrompts(false)

	logger.TaskDebug(ctx, "task-1", "prompt sent to agent:\nfull ticket text here")
	logger.TaskDebug(ctx, "task-1", "$ claude --model claude-sonnet-5 -p ...")
	logger.TaskDebug(ctx, "task-1", "[assistant] working on it")

	logs, err := dbClient.GetTaskLogs(ctx, "task-1", 100)
	if err != nil {
		t.Fatalf("GetTaskLogs: %v", err)
	}
	for _, l := range logs {
		if strings.HasPrefix(l.Message, "prompt sent to agent:") {
			t.Errorf("prompt entry must not reach DB, got: %q", l.Message)
		}
	}
	if len(logs) != 2 {
		t.Errorf("DB log count = %d, want 2 (non-prompt entries only)", len(logs))
	}
}

// A write that would push apiary.log past the size limit must rotate the file
// into a .1 backup and start a fresh one, shifting older backups up a slot and
// dropping the oldest beyond MaxBackups.
func TestRotate_SizeLimitShiftsBackups(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// 1 MB limit, keep 2 backups, no age pruning.
	logger, err := New(dir, nil, LevelInfo, Rotation{MaxSizeMB: 1, MaxBackups: 2, MaxAgeDays: -1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer logger.Close()

	// Each INFO line is ~1 KB of payload; ~3.2 MB total forces >= 2 rotations.
	payload := strings.Repeat("x", 1024)
	for i := 0; i < 3200; i++ {
		logger.Info(ctx, fmt.Sprintf("%05d %s", i, payload), "test")
	}

	logPath := filepath.Join(dir, "apiary.log")
	limit := int64(1 * 1024 * 1024)

	fi, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("apiary.log missing after rotation: %v", err)
	}
	if fi.Size() > limit+2048 {
		t.Errorf("apiary.log size = %d, want <= ~%d", fi.Size(), limit)
	}
	for _, backup := range []string{"apiary.log.1", "apiary.log.2"} {
		if _, err := os.Stat(filepath.Join(dir, backup)); err != nil {
			t.Errorf("expected backup %s: %v", backup, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "apiary.log.3")); err == nil {
		t.Errorf("apiary.log.3 exists, want at most MaxBackups=2 backups")
	}
}

// With MaxSizeMB <= 0 rotation is disabled: the file keeps growing and no
// backups appear.
func TestRotate_DisabledKeepsSingleFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	logger, err := New(dir, nil, LevelInfo, Rotation{MaxSizeMB: -1, MaxBackups: 2, MaxAgeDays: -1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer logger.Close()

	payload := strings.Repeat("x", 1024)
	for i := 0; i < 100; i++ {
		logger.Info(ctx, payload, "test")
	}

	if _, err := os.Stat(filepath.Join(dir, "apiary.log.1")); err == nil {
		t.Errorf("rotation disabled but apiary.log.1 was created")
	}
}

// Startup must prune rotated backups and per-task logs older than MaxAgeDays,
// while keeping recent files.
func TestNew_PrunesOldBackupsAndTaskLogs(t *testing.T) {
	dir := t.TempDir()
	taskDir := filepath.Join(dir, "tasks")
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		t.Fatalf("mkdir tasks: %v", err)
	}

	old := time.Now().AddDate(0, 0, -40)
	files := map[string]time.Time{
		filepath.Join(dir, "apiary.log.1"):     old,
		filepath.Join(taskDir, "old-task.log"): old,
		filepath.Join(taskDir, "new-task.log"): time.Now(),
	}
	for path, mtime := range files {
		if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatalf("chtimes %s: %v", path, err)
		}
	}

	logger, err := New(dir, nil, LevelInfo, Rotation{MaxSizeMB: 50, MaxBackups: 5, MaxAgeDays: 30})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer logger.Close()

	for _, gone := range []string{filepath.Join(dir, "apiary.log.1"), filepath.Join(taskDir, "old-task.log")} {
		if _, err := os.Stat(gone); err == nil {
			t.Errorf("%s should have been pruned", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(taskDir, "new-task.log")); err != nil {
		t.Errorf("recent task log was pruned: %v", err)
	}
}
