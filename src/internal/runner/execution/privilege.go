package execution

import (
	"fmt"
	"strings"
)

// checkPrivilege returns an error when uid is 0 (root) and allowRoot is false.
// Extracted from Run() so it can be tested without requiring a real root process.
func checkPrivilege(uid int, allowRoot bool) error {
	if uid == 0 && !allowRoot {
		return fmt.Errorf("cli runner: refusing to launch agent as root (uid 0); " +
			"set allow_root: true in the runner config to opt in — " +
			"running as root with untrusted prompt content (e.g. Jira/GitHub issues) " +
			"maximises the blast radius of prompt injection")
	}
	return nil
}

// filteredEnv returns a subset of environ containing only entries whose name
// appears in passlist. The comparison is case-sensitive to match os.Environ()
// conventions on Unix.
func filteredEnv(environ []string, passlist []string) []string {
	allowed := make(map[string]struct{}, len(passlist))
	for _, name := range passlist {
		allowed[name] = struct{}{}
	}
	out := make([]string, 0, len(passlist))
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if _, ok := allowed[name]; ok {
			out = append(out, kv)
		}
	}
	return out
}
