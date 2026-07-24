//go:build !windows

package plugin

import (
	"fmt"
	"os"
)

// checkDirOwnerOnly returns a warning error if path has group- or world-write
// bits set. Attackers with group/world write access to a plugin directory can
// plant or replace executables; refusing to load from such directories is the
// stopgap until binary signing is in place.
func checkDirOwnerOnly(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return nil // ReadDir already handled existence; ignore stat failures here
	}
	if perm := info.Mode().Perm(); perm&0o022 != 0 {
		return fmt.Errorf("plugin directory %q is group- or world-writable (mode %04o); skipping for security — run: chmod o-w,g-w %q", path, perm, path)
	}
	return nil
}
