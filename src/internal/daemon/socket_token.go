package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SocketTokenPath returns the path of the socket auth token file for the given
// data directory. The token is written there by the daemon on first start.
func SocketTokenPath(dataDir string) string {
	if dataDir == "" {
		return "/tmp/apiary.socket.token"
	}
	return filepath.Join(dataDir, "socket.token")
}

// LoadOrCreateSocketToken reads the socket auth token from disk. If no token
// file exists, a fresh 256-bit random token is generated, persisted with mode
// 0600, and returned. When configSecret is non-empty it is used directly and
// no file is written.
func LoadOrCreateSocketToken(dataDir, configSecret string) (string, error) {
	if configSecret != "" {
		return configSecret, nil
	}
	path := SocketTokenPath(dataDir)
	if data, err := os.ReadFile(path); err == nil {
		if t := strings.TrimSpace(string(data)); t != "" {
			return t, nil
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate socket token: %w", err)
	}
	token := hex.EncodeToString(raw)
	if err := os.WriteFile(path, []byte(token+"\n"), 0600); err != nil {
		return "", fmt.Errorf("write socket token: %w", err)
	}
	return token, nil
}

// ReadSocketToken reads the socket auth token from the token file. It is used
// by CLI commands and the dashboard to authenticate against the control plane.
// Returns "" when the token file is absent or unreadable (daemon not started).
func ReadSocketToken(dataDir string) (string, error) {
	data, err := os.ReadFile(SocketTokenPath(dataDir))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
