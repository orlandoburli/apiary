package execution

import (
	"os"
	"testing"

	"github.com/orlandoburli/apiary/internal/model"
)

// TestCheckPrivilege_NonRoot verifies that a non-root process (uid != 0) is
// always allowed, regardless of the AllowRoot setting.
func TestCheckPrivilege_NonRoot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("test must run as non-root")
	}
	for _, tc := range []struct {
		name    string
		profile *model.PrivilegeProfile
	}{
		{"nil profile", nil},
		{"allow_root false", &model.PrivilegeProfile{AllowRoot: false}},
		{"allow_root true", &model.PrivilegeProfile{AllowRoot: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkPrivilege(tc.profile); err != nil {
				t.Errorf("expected nil error for non-root process, got: %v", err)
			}
		})
	}
}

// TestCheckPrivilege_Root verifies the root guard logic. Because unit tests
// cannot change uid, this test is skipped unless already running as root.
func TestCheckPrivilege_Root(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("test requires root (uid 0)")
	}
	t.Run("nil profile rejects root", func(t *testing.T) {
		if err := checkPrivilege(nil); err == nil {
			t.Error("expected error for root execution with nil profile")
		}
	})
	t.Run("allow_root false rejects root", func(t *testing.T) {
		if err := checkPrivilege(&model.PrivilegeProfile{AllowRoot: false}); err == nil {
			t.Error("expected error for root execution with allow_root=false")
		}
	})
	t.Run("allow_root true permits root", func(t *testing.T) {
		if err := checkPrivilege(&model.PrivilegeProfile{AllowRoot: true}); err != nil {
			t.Errorf("expected nil error with allow_root=true, got: %v", err)
		}
	})
}

func TestApplyPrivilegeEnv_NilProfile(t *testing.T) {
	env := []string{"HOME=/root", "PATH=/usr/bin", "SECRET=abc"}
	got := applyPrivilegeEnv(env, nil)
	if len(got) != len(env) {
		t.Errorf("nil profile: want %d entries, got %d", len(env), len(got))
	}
}

func TestApplyPrivilegeEnv_StripEnv(t *testing.T) {
	env := []string{"HOME=/root", "SECRET=abc", "PATH=/usr/bin", "TOKEN=xyz"}
	profile := &model.PrivilegeProfile{
		StripEnv: []string{"secret", "TOKEN"}, // case-insensitive
	}
	got := applyPrivilegeEnv(env, profile)
	for _, entry := range got {
		key := envKey(entry)
		if key == "SECRET" || key == "TOKEN" {
			t.Errorf("strip_env: key %q must not appear in output, got entries: %v", key, got)
		}
	}
	if len(got) != 2 {
		t.Errorf("strip_env: want 2 entries (HOME, PATH), got %d: %v", len(got), got)
	}
}

func TestApplyPrivilegeEnv_Allowlist(t *testing.T) {
	env := []string{"HOME=/root", "PATH=/usr/bin", "SECRET=abc", "RUNNER=claude"}
	profile := &model.PrivilegeProfile{
		EnvAllowlist: []string{"path", "RUNNER"}, // case-insensitive
	}
	got := applyPrivilegeEnv(env, profile)
	for _, entry := range got {
		key := envKey(entry)
		if key != "PATH" && key != "RUNNER" {
			t.Errorf("allowlist: unexpected key %q in output: %v", key, got)
		}
	}
	if len(got) != 2 {
		t.Errorf("allowlist: want 2 entries, got %d: %v", len(got), got)
	}
}

func TestApplyPrivilegeEnv_AllowlistAndStrip(t *testing.T) {
	env := []string{"HOME=/root", "PATH=/usr/bin", "SECRET=abc", "RUNNER=claude"}
	profile := &model.PrivilegeProfile{
		EnvAllowlist: []string{"PATH", "SECRET"},
		StripEnv:     []string{"SECRET"},
	}
	got := applyPrivilegeEnv(env, profile)
	// SECRET is in both allowlist and strip_env — strip wins
	for _, entry := range got {
		if envKey(entry) == "SECRET" {
			t.Errorf("strip_env must take precedence over allowlist, but SECRET appeared: %v", got)
		}
	}
	if len(got) != 1 || envKey(got[0]) != "PATH" {
		t.Errorf("want [PATH=/usr/bin], got %v", got)
	}
}

func TestApplyPrivilegeEnv_EmptyProfile(t *testing.T) {
	env := []string{"A=1", "B=2"}
	got := applyPrivilegeEnv(env, &model.PrivilegeProfile{})
	if len(got) != len(env) {
		t.Errorf("empty profile: want %d entries, got %d", len(env), len(got))
	}
}
