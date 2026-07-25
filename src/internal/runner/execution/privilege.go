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

// hostEnvPasslist is the explicit set of host environment variables forwarded
// to every agent subprocess. Anything not listed here is stripped before the
// process is spawned, limiting the exfiltration surface of a successful
// prompt-injection attack.
//
// Operator-configured AI credentials (e.g. ANTHROPIC_API_KEY) appear here so
// the CLI subprocess can authenticate without requiring every agent config to
// repeat the key. Per-agent GitHub credentials are never inherited from the
// host — they arrive exclusively via req.Env (populated by agentIdentityEnv /
// stepEnv in the workflow layer), so a broad host GITHUB_TOKEN cannot bleed
// into an agent's git context.
var hostEnvPasslist = []string{
	// Runtime path and identity
	"PATH",
	"HOME",
	"USER",
	"LOGNAME",
	"SHELL",
	// Temporary directories
	"TMPDIR",
	"TMP",
	"TEMP",
	// Terminal type (needed by some interactive sub-tools)
	"TERM",
	"COLORTERM",
	// Locale
	"LANG",
	"LC_ALL",
	"LC_CTYPE",
	"LC_COLLATE",
	"LC_MESSAGES",
	"LC_MONETARY",
	"LC_NUMERIC",
	"LC_TIME",
	// SSH agent forwarding (git operations)
	"SSH_AUTH_SOCK",
	"SSH_AGENT_PID",
	"GIT_SSH_COMMAND",
	"GIT_SSH",
	// XDG base directories
	"XDG_RUNTIME_DIR",
	"XDG_CONFIG_HOME",
	"XDG_DATA_HOME",
	"XDG_CACHE_HOME",
	// Anthropic CLI credentials (required by the Claude CLI subprocess)
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"ANTHROPIC_BASE_URL",
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
