package execution

import (
	"fmt"
	"os"
)

// ErrRootPrivilege is returned by checkPrivilegeCeiling when the daemon is
// running as root (uid 0) and the runner has not explicitly opted in.
// Running agent CLIs as root is unsafe: a successful prompt-injection from
// untrusted issue content executes at root privilege on the host.
var ErrRootPrivilege = fmt.Errorf(
	"cli runner: refusing to launch agent as root (uid 0); " +
		"run the daemon as a non-root user, or set allow_root: true on the runner " +
		"to acknowledge the risk and opt in explicitly",
)

// checkPrivilegeCeiling blocks agent launch when the process is root and the
// runner has not opted in. This caps the privilege ceiling independent of
// container sandboxing: sandboxing provides isolation, but this check ensures
// that even a sandboxless runner never silently executes at root.
func checkPrivilegeCeiling(allowRoot bool) error {
	if os.Getuid() == 0 && !allowRoot {
		return ErrRootPrivilege
	}
	return nil
}
