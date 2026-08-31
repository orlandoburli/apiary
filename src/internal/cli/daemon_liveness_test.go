package cli

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// serveHealthSocket starts a unix-socket server answering /health the way the
// daemon's control server does, and points APIARY_SOCKET at it.
func serveHealthSocket(t *testing.T) {
	t.Helper()

	// macOS caps unix socket paths at ~104 bytes, and t.TempDir() under
	// TMPDIR can already be long, so keep the name short.
	dir, err := os.MkdirTemp("", "apsock")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	path := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	t.Setenv("APIARY_SOCKET", path)
}

// TestRefuseIfDaemonRunning_BlocksWhenServing is the guard for #468: a data
// migration must not run while a daemon is writing. MigrateData rewrites rows
// and rebuildApprovalRequestsAttempt drops and recreates a table, so a row the
// daemon writes mid-rebuild is lost.
func TestRefuseIfDaemonRunning_BlocksWhenServing(t *testing.T) {
	serveHealthSocket(t)

	err := refuseIfDaemonRunning(context.Background())
	if err == nil {
		t.Fatal("expected a refusal while the daemon socket is serving")
	}

	// The operator must be told where to look and what to do.
	msg := err.Error()
	if !strings.Contains(msg, "apiary service stop") {
		t.Errorf("error should name the fix, got: %s", msg)
	}
	if !strings.Contains(msg, ".sock") {
		t.Errorf("error should name the socket probed, got: %s", msg)
	}
}

// TestRefuseIfDaemonRunning_AllowsWhenAbsent covers the normal path: no daemon,
// so the migration proceeds. A stale socket file left by a crashed daemon also
// lands here, because a dial to it fails.
func TestRefuseIfDaemonRunning_AllowsWhenAbsent(t *testing.T) {
	dir, err := os.MkdirTemp("", "apsock")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	t.Setenv("APIARY_SOCKET", filepath.Join(dir, "absent.sock"))

	if err := refuseIfDaemonRunning(context.Background()); err != nil {
		t.Fatalf("expected no refusal when nothing is serving, got: %v", err)
	}
}

// TestRefuseIfDaemonRunning_StaleSocketFileIsNotAlive pins that a leftover
// socket *file* with nothing behind it does not block a migration — otherwise a
// crashed daemon would wedge every future upgrade.
func TestRefuseIfDaemonRunning_StaleSocketFileIsNotAlive(t *testing.T) {
	dir, err := os.MkdirTemp("", "apsock")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	path := filepath.Join(dir, "stale.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	_ = ln.Close() // leaves the file behind on some platforms

	t.Setenv("APIARY_SOCKET", path)

	if err := refuseIfDaemonRunning(context.Background()); err != nil {
		t.Fatalf("a stale socket file must not look like a live daemon, got: %v", err)
	}
}
