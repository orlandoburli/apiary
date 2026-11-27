package cli

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/daemon"
)

// serveReloadSocket answers POST /reload with the given status and body the way
// the daemon does, and points APIARY_SOCKET at it.
func serveReloadSocket(t *testing.T, status int, result daemon.ReloadResult) {
	t.Helper()

	// macOS caps unix socket paths at ~104 bytes, and t.TempDir() under TMPDIR
	// can already be long, so keep the directory name short.
	dir, err := os.MkdirTemp("", "aprl")
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
	mux.HandleFunc("/reload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(result)
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	t.Setenv("APIARY_SOCKET", path)
}

func TestReloadCommandReportsAppliedDiff(t *testing.T) {
	serveReloadSocket(t, http.StatusOK, daemon.ReloadResult{
		Applied:         true,
		Workflows:       daemon.ChangeSet{Added: []string{"wf-b"}, Changed: []string{"wf-a"}},
		SettingsChanged: []string{"log_level"},
		RequiresRestart: []string{"log_max_age_days"},
	})

	var out bytes.Buffer
	if code := runReload(false, &out); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	got := out.String()
	for _, want := range []string{
		"config reloaded",
		"workflows: +wf-b ~wf-a",
		"settings: log_level",
		"requires restart: log_max_age_days",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestReloadCommandRejectedExitsOne(t *testing.T) {
	serveReloadSocket(t, http.StatusConflict, daemon.ReloadResult{
		Applied: false,
		Error:   `source adapters: source "github": connect: token expired`,
		Sources: daemon.ChangeSet{Added: []string{"github"}},
	})

	var out bytes.Buffer
	if code := runReload(false, &out); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}

	got := out.String()
	for _, want := range []string{
		"reload rejected",
		"source adapters: source",
		"sources: +github",
		"previous configuration is still running",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestReloadCommandJSON(t *testing.T) {
	serveReloadSocket(t, http.StatusOK, daemon.ReloadResult{
		Applied:   true,
		Workflows: daemon.ChangeSet{Changed: []string{"wf-a"}},
	})

	var out bytes.Buffer
	if code := runReload(true, &out); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	var result daemon.ReloadResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode --json output: %v\n%s", err, out.String())
	}
	if !result.Applied || len(result.Workflows.Changed) != 1 {
		t.Errorf("decoded result = %+v, want applied with one changed workflow", result)
	}
}

func TestReloadCommandTransportErrorExitsOne(t *testing.T) {
	t.Setenv("APIARY_SOCKET", filepath.Join(t.TempDir(), "absent.sock"))

	var out bytes.Buffer
	if code := runReload(false, &out); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(out.String(), "Cannot reload") {
		t.Errorf("output should explain the failure:\n%s", out.String())
	}
}
