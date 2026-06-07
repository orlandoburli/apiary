package execution

import (
	"strings"
	"testing"
)

func TestDetectRateLimitRejection_Rejected(t *testing.T) {
	line := `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1780731000,"rateLimitType":"five_hour","overageStatus":"rejected","overageDisabledReason":"org_level_disabled","isUsingOverage":false}}`
	resetsAt, rejected := detectRateLimitRejection(line)
	if !rejected {
		t.Fatal("expected rejected=true")
	}
	if resetsAt != 1780731000 {
		t.Errorf("resetsAt = %d, want 1780731000", resetsAt)
	}
}

func TestDetectRateLimitRejection_NotRejected(t *testing.T) {
	// "allowed" and "allowed_warning" are normal — not rejections. The presence
	// of overageStatus:"rejected" must NOT be mistaken for a rejected run.
	cases := []string{
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1780731000,"overageStatus":"rejected"}}`,
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning","resetsAt":1780731000,"utilization":0.9}}`,
	}
	for _, line := range cases {
		if _, rejected := detectRateLimitRejection(line); rejected {
			t.Errorf("expected rejected=false for %q", line)
		}
	}
}

func TestDetectRateLimitRejection_OtherLines(t *testing.T) {
	cases := []string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`,
		`{"type":"result","subtype":"success","result":"done"}`,
		`plain text, not json`,
		`{"type":"rate_limit_event"}`, // no rate_limit_info
		``,
	}
	for _, line := range cases {
		if _, rejected := detectRateLimitRejection(line); rejected {
			t.Errorf("expected rejected=false for %q", line)
		}
	}
}

func TestErrorDetail(t *testing.T) {
	if got := errorDetail("   "); got != "" {
		t.Errorf("blank input: got %q, want empty", got)
	}
	if got := errorDetail("boom\nstack trace"); got != "boom stack trace" {
		t.Errorf("newline folding: got %q", got)
	}
	long := strings.Repeat("x", 500)
	got := errorDetail(long)
	if len([]rune(got)) > 301 { // 300 chars + the ellipsis rune
		t.Errorf("not capped: len=%d", len([]rune(got)))
	}
	if !strings.HasPrefix(got, "…") {
		t.Errorf("expected ellipsis prefix when capped, got %q", got[:10])
	}
}
