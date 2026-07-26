package execution

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// Environment handling for agent subprocesses.
//
// Two goals, both flagged by review of the first attempt (PR #254):
//
//  1. The agent CLI must keep the credentials it legitimately needs — chiefly
//     its LLM provider key (ANTHROPIC_*, OPENAI_*, opencode, cursor, …). The
//     first attempt stripped those and broke every run. We retain them via a
//     curated family prefix list plus an operator-configurable passthrough.
//  2. Unrelated host secrets the daemon happens to hold (AWS_*, other
//     integrations' tokens, etc.) must NOT be inherited by the agent — on a
//     successful prompt injection they would be exfiltratable. Everything not
//     explicitly allowed is dropped.
//
// Per-task credentials (req.Env, e.g. a scoped GITHUB_TOKEN) are always overlaid
// on top and, in the sandbox path, are passed to the container by NAME only so
// their values never appear in the host process table (argv).

// systemEnvAllow is the set of non-secret host variables the agent process needs
// to function (PATH, locale, TLS trust store, CLI config dirs, etc.).
var systemEnvAllow = map[string]bool{
	"PATH": true, "HOME": true, "SHELL": true, "USER": true, "LOGNAME": true,
	"TERM": true, "TZ": true, "LANG": true, "LC_ALL": true, "LC_CTYPE": true,
	"TMPDIR": true, "TEMP": true, "TMP": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "CURL_CA_BUNDLE": true,
	"SSH_AUTH_SOCK": true, "DOCKER_HOST": true,
	"XDG_CONFIG_HOME": true, "XDG_CACHE_HOME": true, "XDG_DATA_HOME": true,
}

// providerEnvPrefixes matches whole families of LLM/agent-CLI provider
// credentials so a newly-added variable in a known family is covered without a
// code change. Deliberately excludes broad cloud families (AWS_, GCP service
// keys) that are typically unrelated daemon secrets — those must be opted in via
// env_passthrough if an agent genuinely needs them.
var providerEnvPrefixes = []string{
	"ANTHROPIC_", "CLAUDE_",
	"OPENAI_", "AZURE_OPENAI_",
	"OPENCODE_", "CURSOR_",
	"GEMINI_", "GOOGLE_GENERATIVE_",
	"OPENROUTER_", "GROQ_", "MISTRAL_", "DEEPSEEK_", "XAI_",
}

// envAllow decides whether a host variable name may be inherited by the agent.
// passthrough entries are exact names or a trailing-"*" prefix (e.g. "MYCORP_*").
func envAllow(name string, passthrough []string) bool {
	if systemEnvAllow[name] {
		return true
	}
	for _, p := range providerEnvPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	for _, entry := range passthrough {
		if strings.HasSuffix(entry, "*") {
			if strings.HasPrefix(name, strings.TrimSuffix(entry, "*")) {
				return true
			}
		} else if name == entry {
			return true
		}
	}
	return false
}

// scopedEnv builds the environment for an agent subprocess: the allowed host
// variables plus the explicit per-task overlay. hostEnv is the raw environment
// (os.Environ() form "K=V"); overlay wins on key collisions. It returns the
// "K=V" slice for cmd.Env and, separately, the sorted list of variable names it
// contains (used to forward names into the sandbox container).
func scopedEnv(hostEnv []string, overlay map[string]string, passthrough []string) (env []string, names []string) {
	seen := map[string]string{}
	for _, kv := range hostEnv {
		k, v, ok := strings.Cut(kv, "=")
		if ok && envAllow(k, passthrough) {
			seen[k] = v
		}
	}
	// Overlay is always trusted/needed for the task; it wins over host values.
	for k, v := range overlay {
		seen[k] = v
	}
	names = make([]string, 0, len(seen))
	for k := range seen {
		names = append(names, k)
	}
	sort.Strings(names)
	env = make([]string, 0, len(seen))
	for _, k := range names {
		env = append(env, k+"="+seen[k])
	}
	return env, names
}

// cliSandbox describes Docker container isolation applied to a CliRunner. The
// container isolates the agent from the host filesystem (only the working
// directory is mounted) and runs it as an unprivileged user. Network is left
// enabled by default because coding agents must reach their LLM API and git
// remotes; operators who run agents that need no network may set network:"none".
const (
	// sandboxHome is a writable tmpfs mount used as HOME inside the container,
	// since the rootfs is read-only and a --user uid has no /etc/passwd entry.
	sandboxHome = "/home/apiary"
	tmpfsSize   = "512m"
)

type cliSandbox struct {
	image     string
	user      string   // --user; default: the daemon's own uid:gid (workspace is bind-mounted)
	network   string   // --network; default "bridge" (agents need egress)
	extraArgs []string // appended after flags, before the image
}

// wrapCommand rewrites (binary, argv) to run inside the sandbox container.
//
// Credentials are forwarded with the NAME-only form `--env NAME` (never
// `--env NAME=VALUE`): docker reads each value from its own process environment,
// which the caller sets to the full scoped env. This keeps secret values out of
// the host process table (argv), closing the leak flagged in review of #254.
//
// envNames is the list of variable names to forward (from scopedEnv). Only the
// working directory is bind-mounted, so the agent cannot read host files outside
// its task workspace.
func (s *cliSandbox) wrapCommand(binary string, argv []string, workDir string, envNames []string) (string, []string) {
	user := s.user
	if user == "" {
		// Default to the daemon's own uid:gid, NOT "nobody": the task working
		// directory is bind-mounted from the host and is owned by this user, so a
		// mismatched uid makes every workspace write fail with EACCES. Container
		// isolation here comes from the mount namespace and dropped capabilities,
		// not from being a different uid than the files the agent must edit.
		user = fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	}
	network := s.network
	if network == "" {
		network = "bridge"
	}
	dockerArgs := []string{
		"run", "--rm",
		"--network", network,
		"--user", user,
		"--read-only",
		// The rootfs is read-only, so every path the agent must write needs an
		// explicit tmpfs.
		//
		// "exec" must be stated explicitly: docker MERGES its own tmpfs defaults
		// (nodev,noexec,relatime) rather than replacing them, so omitting it
		// leaves the mount noexec and breaks npx, node native modules, and git
		// hooks. "mode=1777" is likewise required because a tmpfs inherits the
		// mountpoint's existing mode — on any image that pre-creates the
		// directory as root:0755, an unprivileged --user could not write to it.
		"--tmpfs", "/tmp:rw,exec,nosuid,mode=1777,size=" + tmpfsSize,
		"--tmpfs", sandboxHome + ":rw,exec,nosuid,mode=1777,size=" + tmpfsSize,
		// A uid passed via --user has no /etc/passwd entry, so HOME would resolve
		// to "/" — which is read-only. Every agent CLI (claude, opencode, cursor)
		// writes a config directory on startup, so point HOME at the tmpfs above.
		// Not a secret, so the NAME=VALUE form is fine here.
		"--env", "HOME=" + sandboxHome,
		"--cap-drop", "all",
		"--security-opt", "no-new-privileges",
	}
	if workDir != "" {
		dockerArgs = append(dockerArgs, "-v", workDir+":"+workDir, "-w", workDir)
	}
	for _, name := range containerEnvNames(envNames) {
		dockerArgs = append(dockerArgs, "--env", name) // name-only: value via docker's own env
	}
	dockerArgs = append(dockerArgs, s.extraArgs...)
	dockerArgs = append(dockerArgs, s.image, binary)
	dockerArgs = append(dockerArgs, argv...)
	return "docker", dockerArgs
}

// hostOnlyEnv names must never be forwarded INTO the container. Some are actively
// dangerous (DOCKER_HOST points at the host Docker socket — forwarding it from an
// isolation feature would hand the agent a trivial host escape; SSH_AUTH_SOCK is
// a host agent socket). The rest are host paths or identity that are meaningless
// or actively wrong inside the image, which supplies its own (HOME under a
// read-only rootfs is the classic failure).
var hostOnlyEnv = map[string]bool{
	"DOCKER_HOST": true, "SSH_AUTH_SOCK": true,
	"HOME": true, "PATH": true, "SHELL": true, "USER": true, "LOGNAME": true,
	"TMPDIR": true, "TEMP": true, "TMP": true,
	"XDG_CONFIG_HOME": true, "XDG_CACHE_HOME": true, "XDG_DATA_HOME": true,
}

// containerEnvNames filters the scoped env down to the names that should cross
// the container boundary — credentials and task config, not host wiring.
func containerEnvNames(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if !hostOnlyEnv[n] {
			out = append(out, n)
		}
	}
	return out
}

// allowedExtraArgs is an ALLOW-list of docker flags permitted in extra_args.
//
// A deny-list was tried first and proved unsound: docker uses spf13/pflag, so
// "-v/:/hostfs" and "-u0:0" are single tokens that never match a "-v"/"-u"
// entry, and boolean flags accept "--read-only=false", which silently disables
// the headline protection because later flags win. Enumerating what is safe is
// the only version of this check that can be reasoned about.
//
// Values are accepted after a flag that takes one; anything not listed is
// refused with a message naming the flag.
var allowedExtraArgs = map[string]bool{
	"--memory": true, "-m": true, "--memory-swap": true, "--memory-reservation": true,
	"--cpus": true, "--cpu-shares": true, "-c": true, "--cpuset-cpus": true, "--cpuset-mems": true,
	"--pids-limit": true, "--ulimit": true, "--oom-kill-disable": true,
	"--label": true, "-l": true, "--name": true, "--hostname": true, "-h": true,
	"--platform": true, "--pull": true, "--quiet": true, "-q": true,
}

// validateExtraArgs permits only the flags in allowedExtraArgs. It normalizes
// the "--flag=value" form and rejects pflag attached-shorthand ("-v/:/hostfs")
// outright, since that form cannot be distinguished from a value safely.
func validateExtraArgs(args []string) error {
	expectValue := false
	for _, a := range args {
		if expectValue {
			expectValue = false
			continue // this token is the previous flag's value
		}
		if !strings.HasPrefix(a, "-") {
			return fmt.Errorf("sandbox.extra_args: unexpected bare value %q; every entry must start with an allowed flag", a)
		}
		flag, hasInlineValue := a, false
		if i := strings.IndexByte(flag, '='); i >= 0 {
			flag, hasInlineValue = flag[:i], true
		}
		// Reject attached shorthand like "-v/:/hostfs" or "-u0:0": a short flag
		// must be its own token so its value cannot smuggle in a denied option.
		if !strings.HasPrefix(flag, "--") && len(flag) > 2 {
			return fmt.Errorf("sandbox.extra_args: %q uses attached shorthand; write the flag and its value as separate entries", a)
		}
		if !allowedExtraArgs[flag] {
			return fmt.Errorf("sandbox.extra_args: %q is not permitted; only resource limits and labelling flags are allowed (e.g. --memory, --cpus, --pids-limit, --ulimit, --label), because anything else can weaken or disable the sandbox", flag)
		}
		if !hasInlineValue && flagTakesValue[flag] {
			expectValue = true
		}
	}
	if expectValue {
		return fmt.Errorf("sandbox.extra_args: trailing flag is missing its value")
	}
	return nil
}

// flagTakesValue marks allowed flags that consume the following token.
var flagTakesValue = map[string]bool{
	"--memory": true, "-m": true, "--memory-swap": true, "--memory-reservation": true,
	"--cpus": true, "--cpu-shares": true, "-c": true, "--cpuset-cpus": true, "--cpuset-mems": true,
	"--pids-limit": true, "--ulimit": true,
	"--label": true, "-l": true, "--name": true, "--hostname": true, "-h": true,
	"--platform": true, "--pull": true,
}
