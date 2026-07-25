//go:build !windows

package config

import (
	"fmt"
	"os"
)

// checkDotEnvPerms returns a non-nil warning string if the .env file at path
// is readable by group or world. The caller decides how to handle the warning.
// A missing or inaccessible file returns an empty string (no warning).
func checkDotEnvPerms(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return "" // absent or inaccessible; caller handles missing file
	}
	if perm := info.Mode().Perm(); perm&0o044 != 0 {
		return fmt.Sprintf(".env file %q is group- or world-readable (mode %04o); credentials may be visible to other local accounts — fix with: chmod 0600 %q", path, perm, path)
	}
	return ""
}
