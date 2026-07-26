package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"

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

// verifyChecksum checks the executable against the manifest's pinned digest.
// An empty Checksum means "not pinned" and is accepted (backward compatible).
func verifyChecksum(execPath, want string) error {
	want = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(want, "sha256:")))
	if want == "" {
		return nil
	}
	got, err := fileSHA256(execPath)
	if err != nil {
		return fmt.Errorf("checksum executable %q: %w", execPath, err)
	}
	if got != want {
		return fmt.Errorf("executable %q failed integrity check: manifest pins sha256:%s but file is sha256:%s (the plugin binary changed after installation)", execPath, want, got)
	}
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
			return // no unprivileged netns equivalent we can rely on
		}
		unshare, err := exec.LookPath("unshare")
		if err != nil {
			return
		}
		// Probe for real: unprivileged userns + netns may be disabled by sysctl,
		// seccomp, or the container runtime. Only trust an actual success.
		probe := exec.Command(unshare, "-r", "-n", "--", "true")
		if err := probe.Run(); err != nil {
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
	return prefix[0], append(append([]string{}, prefix[1:]...), execPath)
}
