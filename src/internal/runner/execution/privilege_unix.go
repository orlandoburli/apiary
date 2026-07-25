//go:build !windows

package execution

import (
	"fmt"
	"os"
)

// checkPrivilege returns an error if the current process is running as root
// (uid 0) and allowRoot is false. Apiary delegates broad filesystem and shell
// access to the underlying CLI agent, so running that agent as root turns any
// prompt-injection into an unrestricted root execution. Operators who truly need
// root (e.g. container-bootstrapping) must explicitly opt in via
// settings.allow_root: true in apiary.yaml.
func checkPrivilege(allowRoot bool) error {
	if os.Getuid() == 0 && !allowRoot {
		return fmt.Errorf(
			"refusing to launch agent CLI as root (uid 0): running agent CLIs as root " +
				"is unsafe because prompt injection in untrusted task content would execute " +
				"with unrestricted privileges; set settings.allow_root: true in apiary.yaml " +
				"to override (not recommended for production)",
		)
	}
	return nil
}
