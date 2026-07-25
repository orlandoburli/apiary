package execution

import "strings"

// anomalyRule is a single heuristic pattern applied to a tool call.
type anomalyRule struct {
	// tools lists which tool names trigger this rule ("" matches any tool).
	tools []string
	// check returns a non-empty reason string when the input matches.
	check func(toolName, input string) string
}

var anomalyRules = []anomalyRule{
	// Network egress: Bash/shell commands that reach out to external hosts.
	{
		tools: []string{"Bash", "bash", "shell", "computer"},
		check: func(_, input string) string {
			lower := strings.ToLower(input)
			for _, marker := range []string{
				"curl ", "wget ", "netcat ", "ncat ", "nmap ",
				"ssh ", "scp ", "sftp ", "rsync ", "telnet ",
				"python -c", "python3 -c", "perl -e", "ruby -e",
			} {
				if strings.Contains(lower, marker) {
					return "network egress: " + strings.TrimSpace(marker)
				}
			}
			// nc matches at word boundary: start of string, after space or semicolon
			if hasWordPrefix(lower, "nc ") {
				return "network egress: nc"
			}
			return ""
		},
	},
	// Dangerous destructive commands.
	{
		tools: []string{"Bash", "bash", "shell", "computer"},
		check: func(_, input string) string {
			lower := strings.ToLower(input)
			for _, marker := range []string{
				"rm -rf /", "rm -fr /", "sudo rm", "mkfs", "dd if=",
				"chmod +s", "chown root", ":(){:|:&};:", // fork bomb
			} {
				if strings.Contains(lower, marker) {
					return "dangerous command: " + strings.TrimSpace(marker)
				}
			}
			return ""
		},
	},
	// Privilege escalation.
	{
		tools: []string{"Bash", "bash", "shell", "computer"},
		check: func(_, input string) string {
			lower := strings.ToLower(input)
			for _, marker := range []string{"sudo ", "su -", "doas "} {
				if strings.Contains(lower, marker) {
					return "privilege escalation: " + strings.TrimSpace(marker)
				}
			}
			return ""
		},
	},
	// Injection patterns: encoded payloads piped to a shell.
	{
		tools: []string{"Bash", "bash", "shell", "computer"},
		check: func(_, input string) string {
			lower := strings.ToLower(input)
			for _, pattern := range []string{
				"base64 -d", "base64 --decode",
				"| bash", "| sh ",
				"eval $(", `eval "$(`,
				"exec(", "os.system(", "subprocess.call(",
			} {
				if strings.Contains(lower, pattern) {
					return "injection pattern: " + strings.TrimSpace(pattern)
				}
			}
			return ""
		},
	},
	// Sensitive file access — any tool that reads or writes credential paths.
	{
		tools: nil, // applies to all tools
		check: func(_, input string) string {
			lower := strings.ToLower(input)
			for _, path := range []string{
				"/etc/passwd", "/etc/shadow", "/etc/sudoers",
				"/.ssh/", "/.aws/credentials", "/.aws/config",
				"/.gnupg/", "/.netrc",
				"/proc/", "/sys/kernel",
			} {
				if strings.Contains(lower, path) {
					return "sensitive path: " + path
				}
			}
			return ""
		},
	},
	// Writing to system directories via file-editing tools.
	{
		tools: []string{"Write", "Edit", "create_file", "write_file"},
		check: func(_, input string) string {
			lower := strings.ToLower(input)
			for _, path := range []string{
				"/etc/", "/usr/bin/", "/usr/local/bin/",
				"/usr/sbin/", "/bin/", "/sbin/",
				"/lib/", "/lib64/",
			} {
				if strings.Contains(lower, path) {
					return "write to system path: " + path
				}
			}
			return ""
		},
	},
	// Exfiltration via environment variable harvesting.
	{
		tools: []string{"Bash", "bash", "shell", "computer"},
		check: func(_, input string) string {
			lower := strings.ToLower(input)
			// Reading all env vars and passing them somewhere.
			if (strings.Contains(lower, "printenv") || strings.Contains(lower, "env ")) &&
				(strings.Contains(lower, "curl ") || strings.Contains(lower, "wget ") ||
					strings.Contains(lower, "nc ") || strings.Contains(lower, "| base64")) {
				return "possible env var exfiltration"
			}
			return ""
		},
	},
}

// DetectAnomaly inspects a tool invocation for suspicious patterns.
// It returns (true, reason) when an anomaly is detected, (false, "") otherwise.
// The reason is a short human-readable description suitable for alerting.
func DetectAnomaly(toolName, input string) (bool, string) {
	for _, rule := range anomalyRules {
		if !ruleApplies(rule, toolName) {
			continue
		}
		if reason := rule.check(toolName, input); reason != "" {
			return true, reason
		}
	}
	return false, ""
}

// hasWordPrefix reports whether s starts with prefix, or contains prefix
// preceded by a shell word separator (space, semicolon, pipe, or ampersand).
func hasWordPrefix(s, prefix string) bool {
	if strings.HasPrefix(s, prefix) {
		return true
	}
	for _, sep := range []byte{' ', ';', '|', '&'} {
		needle := string(sep) + prefix
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func ruleApplies(rule anomalyRule, toolName string) bool {
	if len(rule.tools) == 0 {
		return true // applies to all tools
	}
	for _, t := range rule.tools {
		if t == toolName {
			return true
		}
	}
	return false
}
