package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startFakeDaemon serves h on a short-path Unix socket. The socket path is kept
// short because macOS caps Unix socket paths at ~104 bytes and t.TempDir() is
// long enough to get close.
func startFakeDaemon(t *testing.T, h http.Handler) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "apiary-mcp")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "apiary.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		_ = os.RemoveAll(dir)
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = os.RemoveAll(dir)
	})
	return path
}

func TestServerFraming(t *testing.T) {
	var out bytes.Buffer
	srv := NewServer(strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":4,"method":"nope"}`,
	}, "\n")+"\n"), &out, nil)
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatalf("serve: %v", err)
	}

	var responses []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var resp map[string]any
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("bad line %q: %v", line, err)
		}
		responses = append(responses, resp)
	}
	// The notification must not produce a response.
	if len(responses) != 4 {
		t.Fatalf("responses=%d, want 4: %s", len(responses), out.String())
	}

	init := responses[0]["result"].(map[string]any)
	if init["protocolVersion"] != protocolVersion {
		t.Fatalf("protocolVersion=%v", init["protocolVersion"])
	}
	if _, ok := init["capabilities"].(map[string]any)["tools"]; !ok {
		t.Fatalf("capabilities=%v", init["capabilities"])
	}
	info := init["serverInfo"].(map[string]any)
	if info["name"] != "apiary" {
		t.Fatalf("serverInfo=%v", info)
	}

	tools := responses[2]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools=%v", tools)
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "ask_human" {
		t.Fatalf("tool=%v", tool)
	}
	schema := tool["inputSchema"].(map[string]any)
	if schema["required"].([]any)[0] != "question" {
		t.Fatalf("schema=%v", schema)
	}

	errObj := responses[3]["error"].(map[string]any)
	if int(errObj["code"].(float64)) != errMethodNotFound {
		t.Fatalf("error=%v", errObj)
	}
}

func TestServerToolsCallAnswered(t *testing.T) {
	var posted createQuestionBody
	mux := http.NewServeMux()
	mux.HandleFunc("/questions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&posted)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "q1"})
	})
	mux.HandleFunc("/questions/q1", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(pollResponse{ID: "q1", Status: "answered", Answer: "main", Reason: "it is stable", AnsweredBy: "alice"})
	})
	socket := startFakeDaemon(t, mux)
	t.Setenv("APIARY_INSTANCE_ID", "wf_1")
	t.Setenv("APIARY_STEP_ID", "review")
	t.Setenv("APIARY_TASK_ID", "task_1")
	t.Setenv("APIARY_AGENT_ID", "coder")

	var out bytes.Buffer
	srv := NewServer(strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"ask_human","arguments":{"question":"Which branch?","options":[{"value":"main","label":"Main"}]}}}`+"\n"), &out, NewDaemonClient(socket))
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatalf("serve: %v", err)
	}

	// The question carries the per-run ids from the environment and defaults
	// allow_free_text to true when the tool omits it.
	if posted.Question != "Which branch?" || posted.WorkflowInstanceID != "wf_1" || posted.StepID != "review" || posted.TaskID != "task_1" || posted.AgentID != "coder" {
		t.Fatalf("posted=%+v", posted)
	}
	if !posted.AllowFreeText {
		t.Fatal("allow_free_text should default to true")
	}

	var resp struct {
		Result struct {
			Content []toolContent `json:"content"`
			IsError bool          `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("response %q: %v", out.String(), err)
	}
	if resp.Result.IsError || len(resp.Result.Content) != 1 {
		t.Fatalf("result=%+v", resp.Result)
	}
	var answer map[string]string
	if err := json.Unmarshal([]byte(resp.Result.Content[0].Text), &answer); err != nil {
		t.Fatalf("content %q: %v", resp.Result.Content[0].Text, err)
	}
	if answer["answer"] != "main" || answer["reason"] != "it is stable" || answer["answered_by"] != "alice" {
		t.Fatalf("answer=%v", answer)
	}
}

// A question nobody answers must return promptly and as a normal (non-error)
// tool result: the agent continues, it never hangs waiting for a human.
func TestServerToolsCallTimeoutReturns(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/questions", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "q1"})
	})
	mux.HandleFunc("/questions/q1", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(pollResponse{ID: "q1", Status: "pending"})
	})
	socket := startFakeDaemon(t, mux)
	client := NewDaemonClient(socket)
	client.Deadline = 400 * time.Millisecond

	var out bytes.Buffer
	srv := NewServer(strings.NewReader(`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"ask_human","arguments":{"question":"Anyone?"}}}`+"\n"), &out, client)
	start := time.Now()
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatalf("serve: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timed-out question blocked for %v", elapsed)
	}

	var resp struct {
		Result struct {
			Content []toolContent `json:"content"`
			IsError bool          `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("response %q: %v", out.String(), err)
	}
	if resp.Result.IsError {
		t.Fatal("a timeout must not be reported as a tool error")
	}
	if len(resp.Result.Content) != 1 || !strings.Contains(resp.Result.Content[0].Text, "No answer arrived") {
		t.Fatalf("content=%+v", resp.Result.Content)
	}
}

func TestServerToolsCallUnknownTool(t *testing.T) {
	var out bytes.Buffer
	srv := NewServer(strings.NewReader(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"other","arguments":{}}}`+"\n"), &out, nil)
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error.Code != errInvalidParams {
		t.Fatalf("error=%+v output=%s", resp.Error, out.String())
	}
}
