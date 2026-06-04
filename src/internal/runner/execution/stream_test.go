package execution

import (
	"testing"
)

func TestFormatStreamLine_System(t *testing.T) {
	line := `{"type":"system","subtype":"hook_started","model":"claude-opus-4-8"}`
	got, ok := formatStreamLine(line)
	if !ok {
		t.Fatal("expected ok")
	}
	if !contains(got, "hook_started") || !contains(got, "claude-opus-4-8") {
		t.Errorf("unexpected: %q", got)
	}
}

func TestFormatStreamLine_AssistantText(t *testing.T) {
	line := `{"type":"assistant","message":{"content":[{"type":"text","text":"Hello!"}]}}`
	got, ok := formatStreamLine(line)
	if !ok {
		t.Fatal("expected ok")
	}
	if !contains(got, "Hello") {
		t.Errorf("unexpected: %q", got)
	}
}

func TestFormatStreamLine_ToolUse(t *testing.T) {
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls -la"}}]}}`
	got, ok := formatStreamLine(line)
	if !ok {
		t.Fatal("expected ok")
	}
	if !contains(got, "Bash") {
		t.Errorf("unexpected: %q", got)
	}
}

func TestFormatStreamLine_UserToolResult(t *testing.T) {
	line := `{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}`
	got, ok := formatStreamLine(line)
	if !ok {
		t.Fatal("expected ok")
	}
	if !contains(got, "ok") {
		t.Errorf("unexpected: %q", got)
	}
}

func TestFormatStreamLine_Result(t *testing.T) {
	line := `{"type":"result","subtype":"success","num_turns":3,"duration_ms":5000,"result":"Done!","total_cost_usd":0.05}`
	got, ok := formatStreamLine(line)
	if !ok {
		t.Fatal("expected ok")
	}
	if !contains(got, "Done!") || !contains(got, "success") {
		t.Errorf("unexpected: %q", got)
	}
}

func TestFormatStreamLine_RawText(t *testing.T) {
	line := `just a plain text line`
	got, ok := formatStreamLine(line)
	if ok {
		t.Errorf("expected !ok for plain text, got %q", got)
	}
}

func TestFinalResultText(t *testing.T) {
	line := `{"type":"result","result":"final answer"}`
	got, ok := finalResultText(line)
	if !ok || got != "final answer" {
		t.Errorf("finalResultText(%q) = %q, %v", line, got, ok)
	}
}

func TestFinalResultText_NonResult(t *testing.T) {
	line := `{"type":"assistant","message":{}}`
	got, ok := finalResultText(line)
	if ok {
		t.Errorf("expected !ok for non-result, got %q", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
