package log

import (
	"strings"
	"testing"
)

func TestRedactSecretsJWT(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIzN2Y5M2FhYSIsImV4cCI6MX0.abcdefghijklmnop"
	out := redactSecrets("token=" + jwt)
	if strings.Contains(out, jwt) {
		t.Fatalf("JWT leaked: %s", out)
	}
	if !strings.Contains(out, "«redacted-jwt»") {
		t.Fatalf("expected «redacted-jwt» marker, got: %s", out)
	}
}

func TestRedactSecretsGitHubPAT(t *testing.T) {
	token := "ghp_abcdefghijklmnopqrstuvwxyz012345"
	out := redactSecrets("auth: " + token)
	if strings.Contains(out, token) {
		t.Fatalf("GitHub PAT leaked: %s", out)
	}
	if !strings.Contains(out, "«redacted»") {
		t.Fatalf("expected «redacted» marker, got: %s", out)
	}
}

func TestRedactSecretsBearer(t *testing.T) {
	out := redactSecrets("Authorization: Bearer sk-abcdefgh1234")
	if strings.Contains(out, "sk-abcdefgh1234") {
		t.Fatalf("bearer token leaked: %s", out)
	}
	if !strings.Contains(out, "bearer «redacted»") {
		t.Fatalf("expected bearer redaction, got: %s", out)
	}
}

func TestRedactSecretsAWS(t *testing.T) {
	out := redactSecrets("key=AKIAIOSFODNN7EXAMPLE")
	if strings.Contains(out, "AKIAIOSFODNN7EXAMPLE") {
		t.Fatalf("AWS key leaked: %s", out)
	}
}

func TestRedactSecretsPlainTextUnchanged(t *testing.T) {
	msg := "runner started on port 8080 with timeout 30s"
	if got := redactSecrets(msg); got != msg {
		t.Fatalf("plain message was altered: %q", got)
	}
}

func TestSinkReceivesRedactedMessage(t *testing.T) {
	var captured string
	SetSink(func(_, msg string) { captured = msg })
	t.Cleanup(func() { SetSink(nil) })

	jwt := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIzN2Y5M2FhYSIsImV4cCI6MX0.abcdefghijklmnop"
	Info("token is %s", jwt)

	if strings.Contains(captured, jwt) {
		t.Fatalf("JWT leaked into sink: %s", captured)
	}
	if !strings.Contains(captured, "«redacted-jwt»") {
		t.Fatalf("expected redaction in sink, got: %s", captured)
	}
}
