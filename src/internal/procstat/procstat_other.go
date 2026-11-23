//go:build !darwin && !linux

package procstat

import (
	"fmt"
	"runtime"
)

// readAllProcesses is not implemented on this platform.
//
// The measurement reads the system's process table, which each platform
// exposes differently. Rather than report a number that is not measured, this
// says so: the dashboard shows "not available on this platform" instead of a
// figure that would be a guess.
func readAllProcesses() ([]Process, error) {
	return nil, fmt.Errorf("process resource usage is not supported on %s", runtime.GOOS)
}
