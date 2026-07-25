package log

import "testing"

func TestRedactMsg(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		// plain text passes through
		{"hello world", "hello world"},
		// JWT is replaced inline
		{
			"token eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c end",
			"token [redacted-jwt] end",
		},
		// GitHub PAT
		{"auth ghp_abcdefghijklmnopqrstuvwxyz0123456789", "[redacted]"},
		// GitHub fine-grained PAT
		{"using github_pat_ABCD1234", "[redacted]"},
		// Slack bot token
		{"header: xoxb-12345-secret", "[redacted]"},
		// Slack user token
		{"xoxp-token-value", "[redacted]"},
		// Bearer header
		{"Authorization: Bearer sometoken", "[redacted]"},
		// AWS access key
		{"key=AKIAIOSFODNN7EXAMPLE", "[redacted]"},
	}

	for _, tc := range cases {
		got := redactMsg(tc.input)
		if got != tc.want {
			t.Errorf("redactMsg(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
