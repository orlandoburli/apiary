package execution

import (
	"testing"

	"github.com/orlandoburli/apiary/internal/model"
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

func TestAccumulateStreamUsage_Full(t *testing.T) {
	var u model.Usage

	accumulateStreamUsage(`{"type":"message_start","message":{"usage":{"input_tokens":523}}}`, &u)
	if u.InputTokens != 523 {
		t.Errorf("InputTokens = %d, want 523", u.InputTokens)
	}

	accumulateStreamUsage(`{"type":"message_delta","usage":{"output_tokens":142}}`, &u)
	if u.OutputTokens != 142 {
		t.Errorf("OutputTokens = %d, want 142", u.OutputTokens)
	}
	if u.TotalTokens != 523+142 {
		t.Errorf("TotalTokens = %d, want %d", u.TotalTokens, 523+142)
	}

	accumulateStreamUsage(`{"type":"content_block_start","content_block":{"type":"tool_use","name":"Bash","input":{"command":"ls"}}}`, &u)
	accumulateStreamUsage(`{"type":"content_block_start","content_block":{"type":"tool_use","name":"Read","input":{"file":"x"}}}`, &u)
	if u.NumToolCalls != 2 {
		t.Errorf("NumToolCalls = %d, want 2", u.NumToolCalls)
	}

	accumulateStreamUsage(`{"type":"result","subtype":"success","num_turns":4,"duration_ms":12000,"result":"done","total_cost_usd":0.087}`, &u)
	if u.NumTurns != 4 {
		t.Errorf("NumTurns = %d, want 4", u.NumTurns)
	}
	if u.CostUSD != 0.087 {
		t.Errorf("CostUSD = %.4f, want 0.087", u.CostUSD)
	}
}

func TestAccumulateStreamUsage_NoCost(t *testing.T) {
	var u model.Usage
	accumulateStreamUsage(`{"type":"result","subtype":"success","num_turns":2,"duration_ms":5000,"result":"ok"}`, &u)
	if u.CostUSD != 0 {
		t.Errorf("CostUSD = %.4f, want 0 when total_cost_usd not in event", u.CostUSD)
	}
	if u.NumTurns != 2 {
		t.Errorf("NumTurns = %d, want 2", u.NumTurns)
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
