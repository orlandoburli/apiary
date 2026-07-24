//go:build linux

package plugin

import (
	"os"
	"os/exec"
	"syscall"
)

// applySandbox restricts the subprocess according to the plugin's declared security requirements.
// When network is not requested, the process is placed in an unprivileged user+network namespace
// so it has no external network interfaces.
func applySandbox(cmd *exec.Cmd, security SecurityRequirements) {
	if security.Network {
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
