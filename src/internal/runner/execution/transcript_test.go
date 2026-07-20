package execution

import (
	"strings"
	"testing"
	"time"
)

// Event shapes match the real Claude CLI `--output-format stream-json` output.
func TestTranscriptRendersMarkdown(t *testing.T) {
	var buf strings.Builder
	tr := NewTranscript(&buf, TranscriptMeta{
		Title:    "ERP-42 — Fix login",
		Agent:    "dev",
		Model:    "claude-sonnet-5",
		Step:     "implement",
		Instance: "01JXYZ",
		Started:  time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC),
	})

	lines := []string{
		`{"type":"system","subtype":"init","model":"claude-sonnet-5"}`,
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"I should read the auth file first."}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Looking at the login handler."},{"type":"tool_use","name":"Read","input":{"file_path":"auth.go"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"package auth"}]}]}}`,
		`not json at all`,
		`{"type":"result","subtype":"success","result":"Fixed the bug.","num_turns":3,"duration_ms":65000,"total_cost_usd":0.42}`,
	}
	for _, l := range lines {
		tr.Feed(l)
	}

	out := buf.String()
	for _, want := range []string{
		"# ERP-42 — Fix login",
		"**Step:** implement",
		"### 🟢 Session started — `claude-sonnet-5`",
		"### 🧠 Thinking",
		"> I should read the auth file first.",
		"### 💬 Assistant",
		"Looking at the login handler.",
		"### 🔧 Tool: `Read`",
		"\"file_path\": \"auth.go\"",
		"↩️ Tool result",
		"package auth",
		"## ✅ success · 3 turns · 1m5s · $0.4200",
		"Fixed the bug.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("transcript missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "not json at all") {
		t.Errorf("non-JSON line leaked into transcript")
	}
}

func TestTranscriptTruncatesLongToolResults(t *testing.T) {
	var buf strings.Builder
	tr := NewTranscript(&buf, TranscriptMeta{})
	long := strings.Repeat("x", toolResultLimit+500)
	tr.Feed(`{"type":"user","message":{"content":[{"type":"tool_result","content":"` + long + `"}]}}`)
	out := buf.String()
	if !strings.Contains(out, "truncated") {
		t.Fatalf("expected truncation marker, got:\n%s", out)
	}
	if strings.Count(out, "x") > toolResultLimit {
		t.Fatalf("tool result not truncated")
	}
}

func TestTranscriptRedactsJWTs(t *testing.T) {
	var buf strings.Builder
	tr := NewTranscript(&buf, TranscriptMeta{})
	tr.Feed(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"export TOKEN=\"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIzN2Y5M2FhYSIsImV4cCI6MX0.abcdefghijklmnop\""}}]}}`)
	out := buf.String()
	if strings.Contains(out, "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9") {
		t.Fatalf("JWT leaked into transcript:\n%s", out)
	}
	if !strings.Contains(out, "«redacted-jwt»") {
		t.Fatalf("expected redaction marker:\n%s", out)
	}
}

func TestTranscriptFencesBackticks(t *testing.T) {
	var buf strings.Builder
	tr := NewTranscript(&buf, TranscriptMeta{})
	tr.Feed(`{"type":"user","message":{"content":[{"type":"tool_result","content":"code: ` + "```" + `go\\nfmt.Println()\\n` + "```" + `"}]}}`)
	if !strings.Contains(buf.String(), "````") {
		t.Fatalf("expected extended fence around backtick content:\n%s", buf.String())
	}
}
