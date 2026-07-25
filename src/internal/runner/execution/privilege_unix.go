//go:build !windows

package execution

import (
	"fmt"
	"os"
)

// checkPrivilege returns an error when the current process is running as root
// (uid 0) and the caller has not explicitly opted in. Running Apiary as root
// means a successful prompt-injection attack in any untrusted task body
// (Jira/GitHub issue text, PR description, etc.) executes at full-system
// privilege. Opt in only via the security.allow_root config setting or the
// APIARY_ALLOW_ROOT=1 env var — both are documented as strongly discouraged.
func checkPrivilege(allowRoot bool) error {
	if os.Getuid() != 0 {
		return nil
	}
	if allowRoot || os.Getenv("APIARY_ALLOW_ROOT") == "1" {
		return nil
	}
	return fmt.Errorf(
		"refusing to launch agent as root (uid 0): running Apiary as root gives " +
			"untrusted task content full system access; run as an unprivileged user " +
			"or set security.allow_root: true in apiary.yaml (strongly discouraged) " +
			"/ APIARY_ALLOW_ROOT=1 to override",
	)
}
