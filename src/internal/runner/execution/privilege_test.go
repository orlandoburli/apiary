package execution

import (
	"strings"
	"testing"
)

func TestCheckPrivilege(t *testing.T) {
	tests := []struct {
		name      string
		uid       int
		allowRoot bool
		wantErr   bool
	}{
		{"non-root always allowed", 1000, false, false},
		{"non-root with allowRoot true", 1000, true, false},
		{"root blocked by default", 0, false, true},
		{"root permitted when opted in", 0, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkPrivilege(tc.uid, tc.allowRoot)
			if (err != nil) != tc.wantErr {
				t.Errorf("checkPrivilege(%d, %v) err=%v, wantErr=%v", tc.uid, tc.allowRoot, err, tc.wantErr)
			}
		})
	}
}

func TestCheckPrivilege_ErrorMessage(t *testing.T) {
	err := checkPrivilege(0, false)
	if err == nil {
		t.Fatal("expected error for uid=0")
	}
	msg := err.Error()
	if !strings.Contains(msg, "root") {
		t.Errorf("error message should mention root, got: %q", msg)
	}
	if !strings.Contains(msg, "allow_root") {
		t.Errorf("error message should mention allow_root config key, got: %q", msg)
	}
}

func TestFilteredEnv(t *testing.T) {
	environ := []string{
		"HOME=/root",
		"PATH=/usr/bin:/bin",
		"SECRET_TOKEN=hunter2",
		"ANTHROPIC_API_KEY=sk-ant-xxx",
		"TMPDIR=/tmp",
		"LANG=en_US.UTF-8",
	}

	t.Run("passlist filters to named vars only", func(t *testing.T) {
		got := filteredEnv(environ, []string{"HOME", "PATH", "TMPDIR"})
		if len(got) != 3 {
			t.Fatalf("expected 3 entries, got %d: %v", len(got), got)
		}
		for _, kv := range got {
			name, _, _ := strings.Cut(kv, "=")
			switch name {
			case "HOME", "PATH", "TMPDIR":
			default:
				t.Errorf("unexpected var in filtered output: %q", kv)
			}
		}
	})

	t.Run("secrets excluded when not in passlist", func(t *testing.T) {
		got := filteredEnv(environ, []string{"HOME", "PATH"})
		for _, kv := range got {
			if strings.Contains(kv, "SECRET") || strings.Contains(kv, "ANTHROPIC") {
				t.Errorf("secret var leaked into filtered env: %q", kv)
			}
		}
	})

	t.Run("empty passlist returns empty slice", func(t *testing.T) {
		got := filteredEnv(environ, nil)
		if len(got) != 0 {
			t.Errorf("expected empty, got %v", got)
		}
	})

	t.Run("passlist entry absent from environ is silently skipped", func(t *testing.T) {
		got := filteredEnv(environ, []string{"HOME", "NONEXISTENT"})
		if len(got) != 1 {
			t.Errorf("expected 1 entry, got %d: %v", len(got), got)
		}
	})

	t.Run("values with = signs are preserved correctly", func(t *testing.T) {
		env := []string{"COMPLEX=a=b=c", "OTHER=x"}
		got := filteredEnv(env, []string{"COMPLEX"})
		if len(got) != 1 || got[0] != "COMPLEX=a=b=c" {
			t.Errorf("value with = signs mangled: %v", got)
		}
	})
}

// TestReqEnvFiltered verifies that non-allowlisted keys arriving via req.Env
// (the per-agent overlay) are dropped just like non-allowlisted host env vars.
// This guards the credential-leak vector: a non-passlist key must not reach
// the subprocess even when it originates from the req.Env path.
func TestReqEnvFiltered(t *testing.T) {
	hostEnv := []string{
		"HOME=/home/agent",
		"PATH=/usr/bin:/bin",
	}
	reqEnv := map[string]string{
		// Allowlisted: should survive
		"GITHUB_TOKEN": "ghp_per_agent_token",
		// Not allowlisted: must be dropped
		"AWS_SECRET_ACCESS_KEY": "AKIAIOSFODNN7EXAMPLE",
		"DB_PASSWORD":           "supersecret",
	}

	// Simulate the merge that cli.go performs before calling filteredEnv.
	merged := append([]string{}, hostEnv...)
	for k, v := range reqEnv {
		merged = append(merged, k+"="+v)
	}

	got := filteredEnv(merged, envPasslist)

	// GITHUB_TOKEN must pass through (it is in envPasslist).
	foundGH := false
	for _, kv := range got {
		name, val, _ := strings.Cut(kv, "=")
		switch name {
		case "GITHUB_TOKEN":
			foundGH = true
			if val != "ghp_per_agent_token" {
				t.Errorf("GITHUB_TOKEN value corrupted: %q", kv)
			}
		case "AWS_SECRET_ACCESS_KEY", "DB_PASSWORD":
			t.Errorf("non-allowlisted req.Env key leaked into subprocess env: %q", kv)
		}
	}
	if !foundGH {
		t.Error("GITHUB_TOKEN (allowlisted) was unexpectedly dropped from the merged env")
	}
}
