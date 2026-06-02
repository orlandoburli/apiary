package cli

import (
	"strings"
	"testing"
)

func TestFormatStreamLine(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		wantOK  bool
		wantSub string // substring expected in the formatted output
	}{
		{
			name:    "system init",
			line:    `{"type":"system","subtype":"init","model":"claude-sonnet-4-6","cwd":"/repo"}`,
			wantOK:  true,
			wantSub: "[system:init] model=claude-sonnet-4-6",
		},
		{
			name:    "assistant text",
			line:    `{"type":"assistant","message":{"content":[{"type":"text","text":"Let me read the file."}]}}`,
			wantOK:  true,
			wantSub: "[assistant] Let me read the file.",
		},
		{
			name:    "assistant tool_use",
			line:    `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls -la"}}]}}`,
			wantOK:  true,
			wantSub: "[tool→ Bash]",
		},
		{
			name:    "tool result",
			line:    `{"type":"user","message":{"content":[{"type":"tool_result","content":"file1\nfile2"}]}}`,
			wantOK:  true,
			wantSub: "[tool← result]",
		},
		{
			name:    "result success",
			line:    `{"type":"result","subtype":"success","num_turns":3,"duration_ms":64000,"total_cost_usd":0.0123,"result":"Done."}`,
			wantOK:  true,
			wantSub: "[result:success] turns=3",
		},
		{
			name:   "plain text not json",
			line:   "just a regular log line",
			wantOK: false,
		},
		{
			name:   "json without type",
			line:   `{"foo":"bar"}`,
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := formatStreamLine(tt.line)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (got=%q)", ok, tt.wantOK, got)
			}
			if tt.wantOK && !strings.Contains(got, tt.wantSub) {
				t.Errorf("output %q does not contain %q", got, tt.wantSub)
			}
		})
	}
}

func TestTruncateInput(t *testing.T) {
	long := `{"command":"` + strings.Repeat("x", 500) + `"}`
	out := truncateInput([]byte(long))
	if !strings.HasSuffix(out, "…") {
		t.Errorf("expected truncated output to end with ellipsis, got len=%d", len(out))
	}
	if strings.Contains(out, "\n") {
		t.Errorf("expected whitespace collapsed, got newline in %q", out)
	}
}
