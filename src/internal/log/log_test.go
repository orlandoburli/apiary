package log

import (
	"testing"
)

func TestRedactMsgJWT(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"
	got := redactMsg("token=" + jwt)
	if got == "token="+jwt {
		t.Error("JWT was not redacted")
	}
	if jwtPattern.MatchString(got) {
		t.Error("redacted output still contains a JWT-like sequence")
	}
}

func TestRedactMsgGitHubPAT(t *testing.T) {
	secret := "ghp_abcdefghijklmnopqrstuvwxyz012345"
	got := redactMsg("using token " + secret)
	if got == "using token "+secret {
		t.Error("GitHub PAT was not redacted")
	}
}

func TestRedactMsgAWSKey(t *testing.T) {
	secret := "AKIAIOSFODNN7EXAMPLE"
	got := redactMsg(secret)
	if got == secret {
		t.Error("AWS access key ID was not redacted")
	}
}

func TestRedactMsgBearer(t *testing.T) {
	got := redactMsg("Authorization: Bearer supersecrettoken")
	if got == "Authorization: Bearer supersecrettoken" {
		t.Error("Bearer token header was not redacted")
	}
}

func TestRedactMsgPlainText(t *testing.T) {
	plain := "worker started successfully"
	got := redactMsg(plain)
	if got != plain {
		t.Errorf("plain message was modified unexpectedly: %q", got)
	}
}
