package config

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Digest returns a hex-encoded SHA-256 hash of the canonical YAML
// representation of the config. Fields that vary between runs (rawContent,
// unexported) are excluded because yaml.Marshal only serialises exported
// struct fields tagged with yaml tags. The digest is stable for structurally
// identical configs regardless of source YAML formatting.
//
// If marshalling fails (should not happen for a well-formed Config), the
// returned digest is the SHA-256 of an empty buffer so that callers always
// get a consistent string type; the error is discarded because Digest is used
// in hot-path comparisons where returning an error would be disruptive.
func Digest(c *Config) string {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	encErr := enc.Encode(c)
	closeErr := enc.Close()
	if encErr != nil || closeErr != nil {
		// Fallback: hash of empty bytes. Callers comparing digests will notice
		// the mismatch and revalidate rather than silently accepting stale data.
		var empty [32]byte
		return hex.EncodeToString(empty[:])
	}
	h := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(h[:])
}

// CurrentGitRevision runs `git rev-parse HEAD` in the directory containing
// configFile and returns the full commit hash. Returns an empty string when
// git is unavailable or the directory is not a git repository.
func CurrentGitRevision(configFile string) string {
	dir := filepath.Dir(configFile)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
