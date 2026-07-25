//go:build linux

package daemon

import (
	"net"
	"syscall"
)

// peerUID returns the effective UID of the process on the other end of a Unix
// domain socket connection via SO_PEERCRED. Returns (0, false) when the
// connection is not a *net.UnixConn or the syscall fails.
func peerUID(conn net.Conn) (uint32, bool) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	var cred *syscall.Ucred
	var credErr error
	_ = raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if credErr != nil || cred == nil {
		return 0, false
	}
	return cred.Uid, true
}
