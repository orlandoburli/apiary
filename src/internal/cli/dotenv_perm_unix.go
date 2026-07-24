//go:build !windows

package cli

import (
	"fmt"
	"os"

	aplog "github.com/orlandoburli/apiary/internal/log"
)

// warnDotEnvPerms logs a warning if the .env file at path is readable by
// group or world. Credentials in a world/group-readable file can be accessed
// by any local account that shares a group with the process owner.
func warnDotEnvPerms(path string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	if perm := info.Mode().Perm(); perm&0o044 != 0 {
		aplog.Warn("%s", fmt.Sprintf(".env file %q is group- or world-readable (mode %04o); credentials may be exposed to other local accounts — run: chmod 0600 %q", path, perm, path))
	}
}
