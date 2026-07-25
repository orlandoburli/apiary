package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
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

// TokenPath returns the path for the socket auth token file. The file is
// written with mode 0600 so only the daemon owner can read it.
func TokenPath(dataDir string) string {
	if dataDir == "" {
		return "/tmp/apiary.token"
	}
	return filepath.Join(dataDir, "apiary.token")
}

// writeSocketToken generates a fresh 32-byte random token, writes it to the
// token file (mode 0600), and returns the hex-encoded token string.
func writeSocketToken(dataDir string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating socket token: %w", err)
	}
	token := hex.EncodeToString(raw)
	path := TokenPath(dataDir)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", fmt.Errorf("creating token dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(token), 0600); err != nil {
		return "", fmt.Errorf("writing socket token: %w", err)
	}
	return token, nil
}

// ReadSocketToken reads the socket auth token from the token file. CLI commands
// call this before sending mutating requests to the IPC server.
func ReadSocketToken(dataDir string) (string, error) {
	data, err := os.ReadFile(TokenPath(dataDir))
	if err != nil {
		return "", fmt.Errorf("reading socket token: %w", err)
	}
	return string(data), nil
}

// ensureSocketDir creates the directory that will hold the socket file.
func ensureSocketDir(path string) error {
	return os.MkdirAll(filepath.Dir(path), 0700)
}
