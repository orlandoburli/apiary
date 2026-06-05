package cli

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestResetDatabase_RemovesFileAndSidecars(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "apiary.db")

	// Create the DB plus a WAL and SHM sidecar; leave -journal absent.
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("seed %s: %v", p, err)
		}
	}

	removed, err := resetDatabase(dbPath)
	if err != nil {
		t.Fatalf("resetDatabase: %v", err)
	}
	if removed != 3 {
		t.Errorf("removed = %d, want 3 (db + wal + shm)", removed)
	}
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed", p)
		}
	}
}

func TestResetDatabase_NoFiles(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "absent.db")
	removed, err := resetDatabase(dbPath)
	if err != nil {
		t.Fatalf("resetDatabase: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0 when nothing exists", removed)
	}
}

func TestDaemonIsRunning_NoListener(t *testing.T) {
	// Point the socket at a path with no listener.
	t.Setenv("APIARY_SOCKET", filepath.Join(t.TempDir(), "absent.sock"))
	if daemonIsRunning() {
		t.Error("daemonIsRunning() = true, want false (no listener)")
	}
}

func TestDaemonIsRunning_WithListener(t *testing.T) {
	// Unix socket paths are length-limited (~104 bytes on macOS), so use a short
	// /tmp dir rather than the long t.TempDir() path.
	dir, err := os.MkdirTemp("/tmp", "apiary-clear")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	t.Setenv("APIARY_SOCKET", sock)

	if !daemonIsRunning() {
		t.Error("daemonIsRunning() = false, want true (listener present)")
	}
}
