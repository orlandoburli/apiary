//go:build linux

package plugin

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
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
// security requirements.
//
// Network: when security.Network is false (the default), the subprocess is
// placed in an unprivileged user+network namespace (CLONE_NEWUSER|CLONE_NEWNET)
// if the kernel supports it. On hardened kernels where unprivileged user
// namespaces are disabled, a one-time warning is logged and network-namespace
// isolation is skipped; credential leakage is still prevented by the
// environment allowlist in environment().
//
// Filesystem: when security.ReadPaths or security.WritePaths are non-empty, the
// subprocess is launched via a Landlock re-exec trampoline. The host binary
// re-invokes itself with sandbox control env vars; it applies
// landlock_restrict_self() and then exec()s the actual plugin binary.
// The re-exec is a no-op on kernels < 5.13 (Landlock not available).
func applySandbox(cmd *exec.Cmd, security SecurityRequirements) {
	// Network namespace isolation.
	if !security.Network {
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
		if usernsSupported {
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
	}

	// Filesystem isolation via Landlock re-exec trampoline.
	if len(security.ReadPaths) > 0 || len(security.WritePaths) > 0 {
		setupLandlockReexec(cmd, security)
	}
}

// setupLandlockReexec rewrites cmd so that the host binary is exec'd first.
// The host binary detects _APIARY_SANDBOX_EXEC=1, applies Landlock, then
// exec()s the original plugin binary. cmd.Stdin/Stdout/Stderr are untouched
// so the plugin receives the request payload on fd 0 as usual.
func setupLandlockReexec(cmd *exec.Cmd, security SecurityRequirements) {
	self, err := os.Readlink("/proc/self/exe")
	if err != nil {
		// Can't locate the host binary (unusual); skip filesystem sandbox.
		return
	}
	pluginBin := cmd.Path
	pluginRoot := cmd.Dir

	cmd.Env = append(cmd.Env,
		envSandboxExec+"=1",
		envSandboxBin+"="+pluginBin,
		envSandboxRoot+"="+pluginRoot,
		envSandboxRead+"="+strings.Join(security.ReadPaths, pathSep),
		envSandboxWrite+"="+strings.Join(security.WritePaths, pathSep),
	)
	cmd.Path = self
	cmd.Args = []string{filepath.Base(self)}
}
