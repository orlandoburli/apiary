package execution

import (
	"os"
	"strings"
	"testing"
)

func TestHostEnv_stripsSecrets(t *testing.T) {
	// Set a canary secret and a safe var; verify only the safe var survives.
	t.Setenv("AWS_SECRET_ACCESS_KEY", "should-be-stripped")
	t.Setenv("ANTHROPIC_API_KEY", "should-be-stripped")
	t.Setenv("GITHUB_TOKEN", "should-be-stripped")
	t.Setenv("MY_WEBHOOK_SECRET", "should-be-stripped")
	t.Setenv("PATH", "/usr/bin:/bin")

	got := hostEnv()

	for _, kv := range got {
		key, _, _ := strings.Cut(kv, "=")
		switch key {
		case "AWS_SECRET_ACCESS_KEY", "ANTHROPIC_API_KEY", "GITHUB_TOKEN", "MY_WEBHOOK_SECRET":
			t.Errorf("hostEnv leaked secret var %q", key)
		}
	}
	if !containsKey(got, "PATH") {
		t.Error("hostEnv dropped PATH")
	}
}

func TestHostEnv_allowsLocale(t *testing.T) {
	t.Setenv("LC_ALL", "en_US.UTF-8")
	t.Setenv("LC_CTYPE", "en_US.UTF-8")

	got := hostEnv()
	if !containsKey(got, "LC_ALL") {
		t.Error("hostEnv dropped LC_ALL")
	}
	if !containsKey(got, "LC_CTYPE") {
		t.Error("hostEnv dropped LC_CTYPE")
	}
}

func TestHostEnv_allowsAllowedKeys(t *testing.T) {
	for key := range allowedEnvKeys {
		t.Setenv(key, "test-value")
	}

	got := hostEnv()
	for key := range allowedEnvKeys {
		if !containsKey(got, key) {
			t.Errorf("hostEnv dropped allowed key %q", key)
		}
	}
}

func TestHostEnv_noUnsetVarsLeak(t *testing.T) {
	// Verify that a var not in the allow-list is absent even when set.
	_ = os.Unsetenv("DATABASE_URL")
	t.Setenv("DATABASE_URL", "postgres://user:pass@host/db")

	got := hostEnv()
	if containsKey(got, "DATABASE_URL") {
		t.Error("hostEnv leaked DATABASE_URL")
	}
}

func containsKey(env []string, key string) bool {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return true
		}
	}
	return false
}
