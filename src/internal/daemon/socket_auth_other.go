//go:build !linux

package daemon

import "net"

// peerUID is not implemented on this platform; always returns (0, false).
func peerUID(_ net.Conn) (uint32, bool) { return 0, false }
