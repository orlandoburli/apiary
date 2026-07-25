//go:build !windows

package execution

import (
	"os"
	"testing"
)

func TestCheckPrivilege(t *testing.T) {
	uid := os.Getuid()

	if uid == 0 {
		// Running as root: the default (allowRoot=false) must refuse.
		if err := checkPrivilege(false); err == nil {
			t.Error("expected error when running as root with allowRoot=false, got nil")
		}
		// Explicit opt-in must pass.
		if err := checkPrivilege(true); err != nil {
			t.Errorf("expected no error when running as root with allowRoot=true, got: %v", err)
		}
	} else {
		// Not running as root: both paths must succeed.
		if err := checkPrivilege(false); err != nil {
			t.Errorf("unexpected error for non-root uid %d with allowRoot=false: %v", uid, err)
		}
		if err := checkPrivilege(true); err != nil {
			t.Errorf("unexpected error for non-root uid %d with allowRoot=true: %v", uid, err)
		}
	}
}
