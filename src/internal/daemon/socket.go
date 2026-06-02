package daemon

import (
	"os"
	"path/filepath"
)

// SocketPath returns the Unix socket path for the IPC server. It lives in the
// project's data directory (dataDir/apiary.sock) so each project's daemon has
// its own socket and `apiary status` talks to the right one. Overridable with
// APIARY_SOCKET. Falls back to /tmp/apiary.sock if dataDir is empty.
func SocketPath(dataDir string) string {
	if s := os.Getenv("APIARY_SOCKET"); s != "" {
		return s
	}
	if dataDir == "" {
		return "/tmp/apiary.sock"
	}
	return filepath.Join(dataDir, "apiary.sock")
}

// ensureSocketDir creates the directory that will hold the socket file.
func ensureSocketDir(path string) error {
	return os.MkdirAll(filepath.Dir(path), 0700)
}
