package log

import (
	"strings"
	"testing"
)

func TestRedactMessage(t *testing.T) {
	cases := []struct {
		name  string
		input string
		check func(t *testing.T, got string)
	}{
		{
			name:  "plain message unchanged",
			input: "server started on :8080",
			check: func(t *testing.T, got string) {
				if got != "server started on :8080" {
					t.Errorf("unexpected change: %q", got)
				}
			},
		},
		{
			name:  "github PAT redacted",
			input: "using token ghp_AbCdEfGhIjKlMnOpQrStUvWxYz123456",
			check: func(t *testing.T, got string) {
				if strings.Contains(got, "ghp_AbCdEfGhIjKlMnOpQrStUvWxYz123456") {
					t.Error("raw GitHub PAT leaked into output")
				}
				if !strings.Contains(got, "ghp_") {
					t.Error("prefix should be preserved")
				}
				if !strings.Contains(got, "[REDACTED]") {
					t.Error("expected [REDACTED] marker")
				}
			},
		},
		{
			name:  "github fine-grained PAT redacted",
			input: "token=github_pat_11ABCDEF_longtoken12345",
			check: func(t *testing.T, got string) {
				if strings.Contains(got, "github_pat_11ABCDEF_longtoken12345") {
					t.Error("raw token leaked")
				}
				if !strings.Contains(got, "[REDACTED]") {
					t.Error("expected [REDACTED]")
				}
			},
		},
		{
			name:  "slack bot token redacted",
			input: "connecting via xoxb-1234-5678-abcdefghij",
			check: func(t *testing.T, got string) {
				if strings.Contains(got, "xoxb-1234-5678-abcdefghij") {
					t.Error("raw Slack token leaked")
				}
				if !strings.Contains(got, "[REDACTED]") {
					t.Error("expected [REDACTED]")
				}
			},
		},
		{
			name:  "bearer token redacted, label preserved",
			input: "Authorization: Bearer mysecrettoken123",
			check: func(t *testing.T, got string) {
				if strings.Contains(got, "mysecrettoken123") {
					t.Error("raw bearer value leaked")
				}
				if !strings.Contains(got, "Bearer ") {
					t.Error("Bearer prefix should be preserved")
				}
			},
		},
		{
			name:  "AWS access key redacted",
			input: "AKIAIOSFODNN7EXAMPLE failed auth",
			check: func(t *testing.T, got string) {
				if strings.Contains(got, "AKIAIOSFODNN7EXAMPLE") {
					t.Error("raw AWS key leaked")
				}
				if !strings.Contains(got, "[REDACTED]") {
					t.Error("expected [REDACTED]")
				}
			},
		},
		{
			name:  "JWT redacted",
			input: "token: eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1c2VyIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c",
			check: func(t *testing.T, got string) {
				if strings.Contains(got, "eyJhbGciOiJIUzI1NiJ9") {
					t.Error("raw JWT leaked")
				}
				if !strings.Contains(got, "«redacted-jwt»") {
					t.Error("expected «redacted-jwt» marker")
				}
			},
		},
		{
			name:  "sink receives redacted message",
			input: "connecting with ghp_SomeRealToken9876543",
			check: func(t *testing.T, got string) {
				if strings.Contains(got, "ghp_SomeRealToken9876543") {
					t.Error("raw token in sink message")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactMessage(tc.input)
			tc.check(t, got)
		})
	}
}

func TestSinkReceivesRedactedMessage(t *testing.T) {
	var captured string
	SetSink(func(_, msg string) { captured = msg })
	defer SetSink(nil)

	Info("token: ghp_SuperSecretPAT1234567890")

	if strings.Contains(captured, "ghp_SuperSecretPAT1234567890") {
		t.Error("sink received unredacted token")
	}
	if !strings.Contains(captured, "[REDACTED]") {
		t.Errorf("sink message missing [REDACTED], got: %q", captured)
	}
}
