package execution

import (
	"os"
	"strings"
)

// allowedEnvKeys is the set of host environment variable names that are safe
// to inherit by agent subprocesses. Everything not in this set (or matching
// allowedEnvPrefixes) is stripped before the subprocess starts.
//
// Design rationale: agent subprocesses run untrusted (or semi-trusted) code
// that could exfiltrate host secrets via the inherited environment. Stripping
// to an explicit allow-list ensures that credentials — cloud provider keys,
// API tokens, webhook secrets, database URLs — are never silently present in
// the subprocess environment unless the operator has explicitly configured them
// via agent.env / workflow.env / step.env.
//
// Credentials the agent legitimately needs (GITHUB_TOKEN, ANTHROPIC_API_KEY,
// …) must be declared in config and arrive via req.Env, which is overlaid on
// top of the filtered host environment in the runner. See: agent.source_token
// for per-repo fine-grained tokens (preferred over a broad host GITHUB_TOKEN).
var allowedEnvKeys = map[string]bool{
	// Process execution essentials.
	"PATH":  true,
	"HOME":  true,
	"USER":  true,
	"UID":   true,
	"SHELL": true,
	// POSIX / glibc alternate user-identity names.
	"LOGNAME":  true,
	"USERNAME": true,
	// Terminal / colour output.
	"TERM":         true,
	"TERM_PROGRAM": true,
	"COLORTERM":    true,
	"NO_COLOR":     true,
	// Locale (root key; LC_* prefix is covered by allowedEnvPrefixes).
	"LANG": true,
	// Temporary directories.
	"TMPDIR":  true,
	"TMP":     true,
	"TEMP":    true,
	"TEMPDIR": true,
	// SSH agent socket — agents that push over SSH (git push) need this.
	"SSH_AUTH_SOCK": true,
	"SSH_AGENT_PID": true,
	// Graphics/display — needed by some GUI-adjacent tools on Linux.
	"DISPLAY":         true,
	"WAYLAND_DISPLAY": true,
	// XDG base directories — used by many CLI tools for config/cache/data.
	"XDG_RUNTIME_DIR": true,
	"XDG_CONFIG_HOME": true,
	"XDG_DATA_HOME":   true,
	"XDG_CACHE_HOME":  true,
	"XDG_STATE_HOME":  true,
	// CI flag — some tooling adjusts output/behaviour in CI environments.
	"CI": true,
}

// allowedEnvPrefixes lists key prefixes for host variables that are safe to
// pass through as a group rather than by individual name.
var allowedEnvPrefixes = []string{
	"LC_", // locale categories: LC_CTYPE, LC_MESSAGES, LC_ALL, etc.
}

// hostEnv returns the filtered subset of os.Environ() that is safe to expose
// to agent subprocesses. Only variables in allowedEnvKeys or matching
// allowedEnvPrefixes are kept; everything else — cloud credentials, API keys,
// database URLs, webhook secrets, and any other ambient secret the daemon
// happens to carry — is stripped.
//
// Callers overlay req.Env on top of the result so that any credential
// explicitly configured for the agent is still present in the subprocess
// environment; see CliRunner.Run.
func hostEnv() []string {
	raw := os.Environ()
	out := make([]string, 0, len(raw))
	for _, kv := range raw {
		key, _, found := strings.Cut(kv, "=")
		if !found {
			continue
		}
		if allowedEnvKeys[key] {
			out = append(out, kv)
			continue
		}
		for _, prefix := range allowedEnvPrefixes {
			if strings.HasPrefix(key, prefix) {
				out = append(out, kv)
				break
			}
		}
	}
	return out
}
