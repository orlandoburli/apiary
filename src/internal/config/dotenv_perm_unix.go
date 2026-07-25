//go:build !windows

package config

import (
	"fmt"
	"os"
)

// checkDotEnvPerms returns an error if the .env file at path is readable by
// group or world. Credentials in a group/world-readable file can be read by
// any local account that shares a group with the process owner.
func checkDotEnvPerms(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return nil // absent or inaccessible; caller handles missing file
	}
	if perm := info.Mode().Perm(); perm&0o044 != 0 {
		return fmt.Errorf(".env file %q is group- or world-readable (mode %04o); credentials may be visible to other local accounts — fix with: chmod 0600 %q", path, perm, path)
	}
	return nil
}
