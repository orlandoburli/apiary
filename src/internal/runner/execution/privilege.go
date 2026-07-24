package execution

import (
	"fmt"
	"os"
	"strings"

	"github.com/orlandoburli/apiary/internal/model"
)

// checkPrivilege enforces the privilege ceiling declared in the profile before
// the agent CLI subprocess is started. It returns an error when:
//   - the current process is root (uid 0) and AllowRoot is false, or
//   - the profile is nil and the process is root (safe default: reject root).
//
// On platforms where os.Getuid() returns -1 (Windows) the root check is
// skipped — there is no uid 0 concept there.
func checkPrivilege(profile *model.PrivilegeProfile) error {
	allowRoot := false
	if profile != nil {
		allowRoot = profile.AllowRoot
	}
	if uid := os.Getuid(); uid == 0 && !allowRoot {
		return fmt.Errorf(
			"privilege ceiling: agent CLI refused to start as root (uid 0); " +
				"set privilege.allow_root: true in the agent or step config to opt in, " +
				"or run apiary as a non-root user (strongly recommended for untrusted-content workflows)",
		)
	}
	return nil
}

// applyPrivilegeEnv filters env (a slice of "KEY=VALUE" strings) according to
// the privilege profile and returns the filtered copy. The original slice is not
// modified.
//
// Filtering rules (applied in order):
//  1. If profile.EnvAllowlist is non-empty, only keys in the allowlist pass.
//  2. Any key in profile.StripEnv is removed (even if it is in the allowlist).
//
// Both EnvAllowlist and StripEnv comparisons are case-insensitive key matches.
// A nil profile returns env unmodified.
func applyPrivilegeEnv(env []string, profile *model.PrivilegeProfile) []string {
	if profile == nil || (len(profile.EnvAllowlist) == 0 && len(profile.StripEnv) == 0) {
		return env
	}

	// Build lookup sets for O(1) membership tests.
	strip := make(map[string]struct{}, len(profile.StripEnv))
	for _, k := range profile.StripEnv {
		strip[strings.ToLower(k)] = struct{}{}
	}
	var allowlist map[string]struct{}
	if len(profile.EnvAllowlist) > 0 {
		allowlist = make(map[string]struct{}, len(profile.EnvAllowlist))
		for _, k := range profile.EnvAllowlist {
			allowlist[strings.ToLower(k)] = struct{}{}
		}
	}

	out := make([]string, 0, len(env))
	for _, entry := range env {
		key := envKey(entry)
		lower := strings.ToLower(key)

		// 1. Allowlist filter: skip unless the key is listed.
		if allowlist != nil {
			if _, ok := allowlist[lower]; !ok {
				continue
			}
		}
		// 2. Strip: remove even if it passed the allowlist.
		if _, ok := strip[lower]; ok {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// envKey returns the key portion of a "KEY=VALUE" string.
func envKey(entry string) string {
	if i := strings.IndexByte(entry, '='); i >= 0 {
		return entry[:i]
	}
	return entry
}
