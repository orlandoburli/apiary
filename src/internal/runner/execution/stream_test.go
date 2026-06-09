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

// Event shapes match the real Claude CLI `--output-format stream-json` output:
// tool calls come from `tool_use` blocks in `assistant` messages, and token
// totals from the final `result` event's usage (which folds in cache tokens).
func TestAccumulateStreamUsage_Full(t *testing.T) {
	var u model.Usage

	// Two assistant turns, each issuing a tool call. The per-message usage is a
	// fallback; the authoritative totals arrive on the result event below.
	accumulateStreamUsage(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}],"usage":{"input_tokens":10,"cache_read_input_tokens":1800,"output_tokens":20}}}`, &u)
	accumulateStreamUsage(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file":"x"}}],"usage":{"input_tokens":12,"cache_read_input_tokens":1900,"output_tokens":30}}}`, &u)
	if u.NumToolCalls != 2 {
		t.Errorf("NumToolCalls = %d, want 2", u.NumToolCalls)
	}

	// The result event's usage is authoritative and includes cache tokens on the
	// input side: input = 10 + 6804(creation) + 18053(read) = 24867.
	accumulateStreamUsage(`{"type":"result","subtype":"success","num_turns":4,"duration_ms":12000,"result":"done","total_cost_usd":0.087,"usage":{"input_tokens":10,"cache_creation_input_tokens":6804,"cache_read_input_tokens":18053,"output_tokens":142}}`, &u)
	if u.InputTokens != 24867 {
		t.Errorf("InputTokens = %d, want 24867 (input + cache creation + cache read)", u.InputTokens)
	}
	if u.OutputTokens != 142 {
		t.Errorf("OutputTokens = %d, want 142", u.OutputTokens)
	}
	if u.TotalTokens != 24867+142 {
		t.Errorf("TotalTokens = %d, want %d", u.TotalTokens, 24867+142)
	}
	// The cache portion of the input is also recorded separately (folded into
	// InputTokens above, broken out here).
	if u.CacheCreationTokens != 6804 {
		t.Errorf("CacheCreationTokens = %d, want 6804", u.CacheCreationTokens)
	}
	if u.CacheReadTokens != 18053 {
		t.Errorf("CacheReadTokens = %d, want 18053", u.CacheReadTokens)
	}
	if u.NumTurns != 4 {
		t.Errorf("NumTurns = %d, want 4", u.NumTurns)
	}
	if u.CostUSD != 0.087 {
		t.Errorf("CostUSD = %.4f, want 0.087", u.CostUSD)
	}
}

// The Cursor agent CLI emits a result event with the same shape but camelCase
// usage keys (inputTokens, outputTokens, cacheWriteTokens, cacheReadTokens).
// Verified against live `cursor-agent -p --output-format stream-json` output.
func TestAccumulateStreamUsage_CursorCamelCase(t *testing.T) {
	var u model.Usage
	accumulateStreamUsage(`{"type":"result","subtype":"success","is_error":false,"result":"hi","usage":{"inputTokens":6,"outputTokens":12,"cacheReadTokens":1500,"cacheWriteTokens":29135}}`, &u)
	// input folds the cache: 6 + 29135(write) + 1500(read) = 30641.
	if u.InputTokens != 30641 {
		t.Errorf("InputTokens = %d, want 30641 (input + cache write + cache read)", u.InputTokens)
	}
	if u.OutputTokens != 12 {
		t.Errorf("OutputTokens = %d, want 12", u.OutputTokens)
	}
	if u.CacheCreationTokens != 29135 {
		t.Errorf("CacheCreationTokens = %d, want 29135 (cacheWriteTokens)", u.CacheCreationTokens)
	}
	if u.CacheReadTokens != 1500 {
		t.Errorf("CacheReadTokens = %d, want 1500", u.CacheReadTokens)
	}
	if u.TotalTokens != 30641+12 {
		t.Errorf("TotalTokens = %d, want %d", u.TotalTokens, 30641+12)
	}
}

// When a run ends without a result event (e.g. killed/timed out), the last
// assistant message's usage is reported rather than zeros.
func TestAccumulateStreamUsage_AssistantFallback(t *testing.T) {
	var u model.Usage
	accumulateStreamUsage(`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":5,"cache_read_input_tokens":1000,"output_tokens":8}}}`, &u)
	if u.InputTokens != 1005 || u.OutputTokens != 8 || u.TotalTokens != 1013 {
		t.Errorf("assistant fallback usage = %d in / %d out / %d total, want 1005/8/1013", u.InputTokens, u.OutputTokens, u.TotalTokens)
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
