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

// envPasslist is the complete set of environment variable names forwarded to
// every agent subprocess. It covers both the host environment (os.Environ) and
// the per-agent overlay (req.Env). Any key not listed here is stripped before
// the process starts, regardless of its source.
//
// Keeping one authoritative list for both sources closes the credential-leak
// vector where a non-allowlisted key could bypass filtering by arriving through
// req.Env instead of the host environment.
var envPasslist = []string{
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
	// Per-agent GitHub credentials (set via req.Env from agentIdentityEnv;
	// NOT inherited from the host to prevent daemon-token bleed)
	"GITHUB_TOKEN",
	"GH_TOKEN",
	// Git commit identity (set via req.Env from agentIdentityEnv)
	"GIT_AUTHOR_NAME",
	"GIT_COMMITTER_NAME",
	"GIT_AUTHOR_EMAIL",
	"GIT_COMMITTER_EMAIL",
	// Apiary memory directory (set via req.Env by withMemoryDir)
	"APIARY_MEMORY_DIR",
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
