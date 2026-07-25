//go:build linux

package plugin

import (
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
)

var (
	usernsSupportOnce sync.Once
	usernsSupported   bool
)

// probeUsernsSupport reports whether unprivileged user namespaces are available
// on this kernel. It checks the two sysctl knobs used by distributions to gate
// the feature: the Debian/Ubuntu-specific unprivileged_userns_clone and the
// generic max_user_namespaces (a value of 0 means the kernel refuses CLONE_NEWUSER).
func probeUsernsSupport() bool {
	if data, err := os.ReadFile("/proc/sys/kernel/unprivileged_userns_clone"); err == nil {
		if strings.TrimSpace(string(data)) == "0" {
			return false
		}
	}
	if data, err := os.ReadFile("/proc/sys/user/max_user_namespaces"); err == nil {
		if strings.TrimSpace(string(data)) == "0" {
			return false
		}
	}
	return true
}

// applySandbox restricts the subprocess according to the plugin's declared
// security requirements. When network access is not declared, the process is
// placed in an unprivileged user+network namespace if the kernel supports it.
// On hardened kernels and container runtimes where unprivileged user namespaces
// are disabled, a warning is logged once and the plugin runs without OS-level
// network-namespace isolation; credential leakage is still prevented by the
// environment allowlist built in environment().
func applySandbox(cmd *exec.Cmd, security SecurityRequirements) {
	if security.Network {
		return
	}
	usernsSupportOnce.Do(func() {
		usernsSupported = probeUsernsSupport()
		if !usernsSupported {
			log.Print("apiary: WARNING: unprivileged user namespaces are unavailable on this kernel; " +
				"plugin network-namespace isolation is disabled. Plugins still run with a " +
				"restricted environment (no host credentials), but OS-level network " +
				"isolation cannot be applied. Consider running on a kernel with " +
				"user namespaces enabled for stronger containment.")
		}
	})
	if !usernsSupported {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWNET | syscall.CLONE_NEWUSER,
		UidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getuid(), Size: 1},
		},
		GidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getgid(), Size: 1},
		},
	}
}
