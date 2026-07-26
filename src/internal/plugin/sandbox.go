package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Executable integrity
//
// A manifest may pin the SHA-256 of its executable. This detects tampering or
// swap-out of a plugin binary after installation (integrity). It is deliberately
// NOT presented as authenticity/signing: the digest lives beside the binary, so
// anyone who can rewrite the executable can also rewrite the manifest. Real
// authenticity would need an out-of-band trusted key.

// fileSHA256 returns the lowercase hex SHA-256 of the file at path.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// normalizeChecksum parses a manifest checksum value. It returns the bare
// lowercase hex digest and whether a pin is present. An absent/blank value means
// "not pinned" (backward compatible); a NON-blank but malformed value is an
// error rather than being silently treated as unpinned.
//
// Normalization order matters: trim and lowercase BEFORE stripping the prefix,
// so "SHA256:<hex>" and " sha256:<hex> " are accepted.
func normalizeChecksum(raw string) (string, bool, error) {
	if strings.TrimSpace(raw) == "" {
		return "", false, nil // unpinned
	}
	v := strings.ToLower(strings.TrimSpace(raw))
	v = strings.TrimSpace(strings.TrimPrefix(v, "sha256:"))
	if len(v) != sha256HexLen {
		return "", false, fmt.Errorf("invalid checksum %q: expected %d hex characters (sha256), got %d", raw, sha256HexLen, len(v))
	}
	if _, err := hex.DecodeString(v); err != nil {
		return "", false, fmt.Errorf("invalid checksum %q: not valid hexadecimal", raw)
	}
	return v, true, nil
}

const sha256HexLen = 64

// verifyChecksum checks the executable against the manifest's pinned digest.
func verifyChecksum(execPath, want string) error {
	digest, pinned, err := normalizeChecksum(want)
	if err != nil {
		return err
	}
	if !pinned {
		return nil
	}
	got, err := fileSHA256(execPath)
	if err != nil {
		return fmt.Errorf("checksum executable %q: %w", execPath, err)
	}
	if got != digest {
		return fmt.Errorf("executable %q failed integrity check: manifest pins sha256:%s but file is sha256:%s (the plugin binary changed after installation)", execPath, digest, got)
	}
	return nil
}

// integrityGuard re-verifies a pinned executable before each invocation, so a
// binary swapped while the daemon is running is caught — boot-only verification
// would miss exactly the post-install tampering the pin exists to detect. The
// hash is recomputed only when the file's size or mtime changed since the last
// successful check, keeping the steady-state cost to one stat().
//
// Known limits (this is tamper-evidence, not a security boundary):
//   - An attacker who can write the executable can also preserve its size and
//     mtime, which would defeat the cache. Anyone with that access can equally
//     rewrite the manifest's pin, so the check targets accidental drift and
//     unsophisticated swaps.
//   - There is an unavoidable TOCTOU window between hashing and exec.
type integrityGuard struct {
	mu       sync.Mutex
	size     int64
	modTime  time.Time
	verified bool
}

func (g *integrityGuard) check(execPath, want string) error {
	if strings.TrimSpace(want) == "" {
		return nil // unpinned
	}
	info, err := os.Stat(execPath)
	if err != nil {
		return fmt.Errorf("stat executable %q: %w", execPath, err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.verified && info.Size() == g.size && info.ModTime().Equal(g.modTime) {
		return nil // unchanged since last successful verification
	}
	if err := verifyChecksum(execPath, want); err != nil {
		g.verified = false
		return err
	}
	g.size, g.modTime, g.verified = info.Size(), info.ModTime(), true
	return nil
}
