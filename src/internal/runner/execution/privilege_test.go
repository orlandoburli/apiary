package execution

import (
	"os"
	"testing"
)

func TestCheckPrivilege_AllowRoot(t *testing.T) {
	// When allowRoot is true the call must not error regardless of uid.
	if err := checkPrivilege(true); err != nil {
		t.Fatalf("checkPrivilege(allowRoot=true): unexpected error: %v", err)
	}
}

func TestCheckPrivilege_EnvOverride(t *testing.T) {
	t.Setenv("APIARY_ALLOW_ROOT", "1")
	if err := checkPrivilege(false); err != nil {
		t.Fatalf("checkPrivilege with APIARY_ALLOW_ROOT=1: unexpected error: %v", err)
	}
}

func TestCheckPrivilege_NonRoot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("test must run as non-root")
	}
	if err := checkPrivilege(false); err != nil {
		t.Fatalf("checkPrivilege on non-root process: unexpected error: %v", err)
	}
}

func TestCheckPrivilege_RootRefused(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("test requires root uid")
	}
	os.Unsetenv("APIARY_ALLOW_ROOT")
	if err := checkPrivilege(false); err == nil {
		t.Fatal("checkPrivilege(allowRoot=false) on root: expected error, got nil")
	}
}
