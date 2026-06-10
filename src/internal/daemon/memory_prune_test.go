package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/memory"
	"github.com/orlandoburli/apiary/internal/model"
)

func TestPruneTaskMemoryOnce(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "prune.db"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	defer dbc.Close()

	ms, err := memory.Open(t.TempDir())
	if err != nil {
		t.Fatalf("memory.Open: %v", err)
	}

	d := &Dispatcher{
		db:       dbc,
		memStore: ms,
		cfg: &config.Config{Settings: config.Settings{
			Memory: config.MemorySettings{Enabled: true, TaskRetention: "1ms"},
		}},
	}

	tasks := dbc.InternalTasks()
	mk := func(id string, state model.TaskState, parent string) {
		t.Helper()
		task := &model.InternalTask{ID: id, ParentTaskID: parent, Title: id, State: state}
		if err := tasks.CreateTask(ctx, task); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		if err := tasks.UpdateTaskState(ctx, id, state); err != nil {
			t.Fatalf("state %s: %v", id, err)
		}
	}
	mk("done-old", model.TaskStateDone, "")
	mk("still-running", model.TaskStateRunning, "")
	mk("done-with-live-child", model.TaskStateDone, "")
	mk("live-child", model.TaskStateRunning, "done-with-live-child")

	for _, id := range []string{"done-old", "still-running", "done-with-live-child", "unknown-task"} {
		if err := ms.AppendTaskNote(id, memory.Note{Content: "note for " + id}); err != nil {
			t.Fatalf("note %s: %v", id, err)
		}
	}

	// Let every UpdatedAt and file mtime fall past the 1ms retention window.
	time.Sleep(20 * time.Millisecond)

	pruned := d.pruneTaskMemoryOnce(ctx)
	if pruned != 2 {
		t.Fatalf("expected 2 pruned (done-old + unknown-task), got %d", pruned)
	}
	if ms.TaskNoteContent("done-old") != "" {
		t.Error("done-old notes should be pruned (terminal past retention)")
	}
	if ms.TaskNoteContent("unknown-task") != "" {
		t.Error("unknown-task notes should be pruned (mtime fallback)")
	}
	if ms.TaskNoteContent("still-running") == "" {
		t.Error("still-running notes must be retained (non-terminal)")
	}
	if ms.TaskNoteContent("done-with-live-child") == "" {
		t.Error("done-with-live-child notes must be retained (live descendant)")
	}
}

func TestWithMemoryDir(t *testing.T) {
	env := withMemoryDir(map[string]string{"A": "1"}, "/mem/root")
	if env["APIARY_MEMORY_DIR"] != "/mem/root" {
		t.Fatalf("memory dir not injected: %v", env)
	}
	// An explicit value at any env scope wins.
	env = withMemoryDir(map[string]string{"APIARY_MEMORY_DIR": "/custom"}, "/mem/root")
	if env["APIARY_MEMORY_DIR"] != "/custom" {
		t.Fatalf("explicit env must win: %v", env)
	}
	// Disabled memory ("" dir) leaves env untouched.
	env = withMemoryDir(map[string]string{}, "")
	if _, ok := env["APIARY_MEMORY_DIR"]; ok {
		t.Fatal("disabled memory must not set the env var")
	}
}
