package execution

import (
	"errors"
	"os"
	"testing"
)

func TestCheckPrivilegeCeiling_NonRoot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("test must run as non-root")
	}
	// Non-root processes should always be allowed, regardless of allowRoot.
	if err := checkPrivilegeCeiling(false); err != nil {
		t.Errorf("expected no error as non-root user, got: %v", err)
	}
	if err := checkPrivilegeCeiling(true); err != nil {
		t.Errorf("expected no error as non-root user with allow_root=true, got: %v", err)
	}
}

func TestCheckPrivilegeCeiling_RootBlocked(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("test must run as root")
	}
	err := checkPrivilegeCeiling(false)
	if err == nil {
		t.Fatal("expected ErrRootPrivilege as root with allow_root=false, got nil")
	}
	if !errors.Is(err, ErrRootPrivilege) {
		t.Errorf("expected ErrRootPrivilege, got: %v", err)
	}
}

func TestCheckPrivilegeCeiling_RootOptIn(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("test must run as root")
	}
	if err := checkPrivilegeCeiling(true); err != nil {
		t.Errorf("expected no error as root with allow_root=true, got: %v", err)
	}
}

func TestCliRunner_Configure_AllowRoot(t *testing.T) {
	r := &CliRunner{}
	if err := r.Configure(map[string]any{
		"command":    "claude",
		"allow_root": true,
	}); err != nil {
		t.Fatalf("Configure error: %v", err)
	}
	if !r.allowRoot {
		t.Error("expected allowRoot=true after allow_root: true config")
	}
}

func TestCliRunner_Configure_AllowRoot_DefaultsFalse(t *testing.T) {
	r := &CliRunner{}
	if err := r.Configure(map[string]any{
		"command": "claude",
	}); err != nil {
		t.Fatalf("Configure error: %v", err)
	}
	if r.allowRoot {
		t.Error("expected allowRoot=false by default (allow_root not set)")
	}
}
