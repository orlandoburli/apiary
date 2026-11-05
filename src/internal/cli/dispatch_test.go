package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/daemon"
)

func TestParseInputPairs(t *testing.T) {
	got, err := parseInputPairs([]string{"scope=q3", "dry_run=true", "note=a=b"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got["scope"] != "q3" || got["dry_run"] != "true" {
		t.Errorf("parsed = %v", got)
	}
	// Only the first '=' separates: a value may legitimately contain more.
	if got["note"] != "a=b" {
		t.Errorf("note = %q, want %q", got["note"], "a=b")
	}
}

func TestParseInputPairs_Empty(t *testing.T) {
	got, err := parseInputPairs(nil)
	if err != nil || got != nil {
		t.Fatalf("got (%v, %v), want (nil, nil) so no input field is sent", got, err)
	}
}

func TestParseInputPairs_Invalid(t *testing.T) {
	for _, bad := range []string{"noequals", "=novalue"} {
		if _, err := parseInputPairs([]string{bad}); err == nil {
			t.Errorf("parseInputPairs(%q) accepted a malformed pair", bad)
		}
	}
}

func sampleRun() daemon.ManualRunResult {
	return daemon.ManualRunResult{
		WorkflowID: "triage", InstanceID: "wf_1790032343887924000_23", TaskID: "task-1",
		CellID: "499", Ref: "#499", SourceID: "apiary", Bypassed: []string{"once"},
	}
}

func TestWriteDispatchText_NamesTheInstance(t *testing.T) {
	var out bytes.Buffer
	writeDispatchText(&out, sampleRun())
	for _, want := range []string{
		"✓ Started workflow triage on #499 (499) in apiary — instance wf_1790032343887924000_23\n",
		"! bypassed guard: once\n",
		"follow it with: apiary instances wf_1790032343887924000_23\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

// A daemon older than the CLI sends no id. The line must not claim one.
func TestWriteDispatchText_WithoutInstance(t *testing.T) {
	res := sampleRun()
	res.InstanceID = ""
	var out bytes.Buffer
	writeDispatchText(&out, res)
	if strings.Contains(out.String(), "instance ") && !strings.Contains(out.String(), "apiary instances\n") {
		t.Errorf("output invents an instance:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "follow it with: apiary instances\n") {
		t.Errorf("output lost the follow hint:\n%s", out.String())
	}
}

func TestWriteDispatchJSON(t *testing.T) {
	var out bytes.Buffer
	if err := writeDispatchJSON(&out, sampleRun()); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	if got["instance_id"] != "wf_1790032343887924000_23" || got["workflow"] != "triage" || got["item"] != "#499" {
		t.Errorf("got %v", got)
	}
	if guards, ok := got["bypassed_guards"].([]any); !ok || len(guards) != 1 {
		t.Errorf("bypassed_guards = %v", got["bypassed_guards"])
	}
}

// Scripts index these without checking: a standalone run still has them all, and
// the guard list is an array rather than null.
func TestWriteDispatchJSON_StandaloneKeepsEveryField(t *testing.T) {
	var out bytes.Buffer
	if err := writeDispatchJSON(&out, daemon.ManualRunResult{WorkflowID: "audit", Standalone: true}); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"instance_id", "workflow", "item", "source", "task_id", "standalone", "concurrent", "bypassed_guards"} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing %q in %s", key, out.String())
		}
	}
	if _, ok := got["bypassed_guards"].([]any); !ok {
		t.Errorf("bypassed_guards = %v, want an array", got["bypassed_guards"])
	}
}

// serveDispatch answers the daemon's control socket with handler.
func serveDispatch(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	// Unix socket paths are length-limited, so not t.TempDir().
	dir, err := os.MkdirTemp("/tmp", "apiary-dispatch")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() { _ = server.Close() })
	t.Setenv("APIARY_SOCKET", sock)
}

// The id crosses the socket as the daemon encodes it and comes out of the command.
func TestDispatchCommand_InstanceIDArrivesOverIPC(t *testing.T) {
	var path, query string
	serveDispatch(t, func(w http.ResponseWriter, r *http.Request) {
		path, query = r.URL.Path, r.URL.RawQuery
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(sampleRun())
	})

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"text", []string{"triage", "--item", "#499"}, "— instance wf_1790032343887924000_23\n"},
		{"json", []string{"triage", "--item", "#499", "--json"}, `"instance_id":"wf_1790032343887924000_23"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newDispatchCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs(tc.args)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("dispatch: %v", err)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Errorf("output lacks %q:\n%s", tc.want, out.String())
			}
			if path != "/workflows/triage/run" || !strings.Contains(query, "item=%23499") {
				t.Errorf("request = %s?%s", path, query)
			}
		})
	}
}

func TestDispatchCommand_JSONFailurePrintsNothing(t *testing.T) {
	serveDispatch(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unknown workflow", http.StatusNotFound)
	})
	// The real os.Stdout, not cmd.SetOut: SetOut also captures cobra's usage text,
	// which goes to stderr in a real run, and stdout is what a script reads.
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = write
	t.Cleanup(func() { os.Stdout = stdout })

	cmd := newDispatchCmd()
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"nope", "--json"})
	runErr := cmd.Execute()
	_ = write.Close()
	os.Stdout = stdout
	got, _ := io.ReadAll(read)

	if runErr == nil || !strings.Contains(runErr.Error(), "unknown workflow") {
		t.Fatalf("err = %v", runErr)
	}
	if len(got) != 0 {
		t.Errorf("stdout = %q, want empty so a script cannot parse a failure", got)
	}
}
