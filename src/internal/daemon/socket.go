package daemon

import (
	"os"
	"path/filepath"
)

// SocketPath returns the Unix socket path for the IPC server.
// Defaults to ~/.apiary/apiary.sock, overridable with APIARY_SOCKET.
func SocketPath() string {
	if s := os.Getenv("APIARY_SOCKET"); s != "" {
		return s
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/apiary.sock"
	}
	return filepath.Join(home, ".apiary", "apiary.sock")
}

// ensureSocketDir creates the directory that will hold the socket file.
func ensureSocketDir(path string) error {
	return os.MkdirAll(filepath.Dir(path), 0700)
}
