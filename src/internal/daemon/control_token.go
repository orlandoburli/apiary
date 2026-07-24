package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

const controlTokenFile = "control.token"

// ControlTokenPath returns the path to the per-project control token file.
func ControlTokenPath(dataDir string) string {
	if dataDir == "" {
		return filepath.Join(os.TempDir(), "apiary-"+controlTokenFile)
	}
	return filepath.Join(dataDir, controlTokenFile)
}

// GenerateControlToken creates a fresh 32-byte random token, writes it to
// ControlTokenPath(dataDir) with mode 0600, and returns it. Replaces any
// existing token so stale copies from a previous run are invalidated.
func GenerateControlToken(dataDir string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate control token: %w", err)
	}
	token := hex.EncodeToString(raw)

	path := ControlTokenPath(dataDir)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", fmt.Errorf("control token dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0600); err != nil {
		return "", fmt.Errorf("write control token: %w", err)
	}
	return token, nil
}

// ReadControlToken reads the token written by GenerateControlToken. Returns an
// empty string (and no error) when the file does not exist so that read-only
// CLI commands still work against a daemon that has not yet written a token.
func ReadControlToken(dataDir string) string {
	data, err := os.ReadFile(ControlTokenPath(dataDir))
	if err != nil {
		return ""
	}
	// Trim any trailing newline written by GenerateControlToken.
	raw := string(data)
	for len(raw) > 0 && (raw[len(raw)-1] == '\n' || raw[len(raw)-1] == '\r') {
		raw = raw[:len(raw)-1]
	}
	return raw
}
