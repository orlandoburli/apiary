package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	aplog "github.com/orlandoburli/apiary/internal/log"
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

// Network isolation
//
// When a manifest declares security.network: false, we deny the plugin network
// access where the platform supports it. Support is probed at runtime rather
// than assumed: the first attempt (PR #255) unconditionally required unprivileged
// user namespaces, which hard-fails on hardened kernels, on macOS, and inside
// many container runtimes (including Apiary's own image).
//
// Enforcement is therefore best-effort and honestly reported: on platforms
// without support we log a clear warning that the declared restriction is not
// enforced, instead of silently pretending it is, or refusing to run at all.

var (
	netIsolationOnce sync.Once
	netIsolationCmd  []string // argv prefix that runs a command without network, nil if unsupported
)

// detectNetIsolation probes once for a usable no-network launcher. It returns an
// argv prefix (e.g. ["unshare", "-r", "-n", "--"]) or nil when the platform
// cannot enforce network isolation for an unprivileged process.
func detectNetIsolation() []string {
	netIsolationOnce.Do(func() {
		if runtime.GOOS != "linux" {
			aplog.Debug("plugin sandbox: network isolation unavailable on %s (no unprivileged netns equivalent)", runtime.GOOS)
			return
		}
		unshare, err := exec.LookPath("unshare")
		if err != nil {
			aplog.Debug("plugin sandbox: network isolation unavailable: %v", err)
			return
		}
		// Probe for real: unprivileged userns + netns may be disabled by sysctl,
		// seccomp, or the container runtime. Only trust an actual success. The
		// probe is bounded so a hung/blocked unshare cannot wedge plugin
		// invocations permanently behind sync.Once.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := exec.CommandContext(ctx, unshare, "-r", "-n", "--", "true").Run(); err != nil {
			aplog.Debug("plugin sandbox: unprivileged netns probe failed (%v); network isolation will not be enforced", err)
			return
		}
		netIsolationCmd = []string{unshare, "-r", "-n", "--"}
	})
	return netIsolationCmd
}

// applyNetworkIsolation returns the (binary, args) to launch execPath with the
// plugin's declared network policy applied. When the manifest allows network, or
// the platform cannot enforce isolation, the command is returned unchanged — in
// the latter case with a warning naming the plugin, so an operator can see that
// a declared restriction is not being enforced on this host.
func applyNetworkIsolation(pluginID, execPath string, allowNetwork bool) (string, []string) {
	if allowNetwork {
		return execPath, nil
	}
	prefix := detectNetIsolation()
	if len(prefix) == 0 {
		aplog.Warn("plugin %q declares security.network:false but this host cannot enforce network isolation (%s, no usable unprivileged netns); the plugin WILL have network access", pluginID, runtime.GOOS)
		return execPath, nil
	}
	// NOTE: "unshare -r" maps the caller to uid 0 INSIDE the new user namespace.
	// That is namespace-local (no privilege on the host) and is what makes the
	// unprivileged netns possible, but plugin authors should not read it as
	// "running as root".
	return prefix[0], append(append([]string{}, prefix[1:]...), execPath)
}
