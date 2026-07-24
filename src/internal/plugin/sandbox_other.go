//go:build !linux

package plugin

import "os/exec"

// applySandbox is a no-op on platforms that do not support unprivileged network namespaces.
// Plugins are still restricted to the minimal environment built by environment().
func applySandbox(_ *exec.Cmd, _ SecurityRequirements) {}
