package execution

import (
	"strings"

	"github.com/orlandoburli/apiary/internal/model"
)

// Anomaly kinds emitted by DetectAnomaly. Used as the "kind" metadata field in
// agent.anomaly execution events.
const (
	AnomalyReverseShell     = "reverse_shell"
	AnomalyNetworkEgress    = "network_egress"
	AnomalyCredentialAccess = "credential_access"
)

// DetectAnomaly checks a recorded agent action for patterns that indicate
// injection or exfiltration. Returns the anomaly kind and a short detail
// string when a pattern is found; found is false for clean actions.
func DetectAnomaly(action model.AgentAction) (kind, detail string, found bool) {
	input := strings.ToLower(action.InputSummary)
	switch normalizeToolName(action.Tool) {
	case "bash", "computer", "execute_bash", "run_command", "terminal", "shell":
		return detectBashAnomaly(input)
	case "web_fetch", "webfetch", "browser", "http_request", "fetch":
		return AnomalyNetworkEgress, "agent issued a web fetch to an external URL", true
	}
	return "", "", false
}

// normalizeToolName lower-cases and strips common prefixes/suffixes so variants
// like "Bash", "mcp__bash", and "execute_bash" all reduce to a canonical form.
func normalizeToolName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	// strip mcp__ prefix (tool routed through an MCP server)
	if after, ok := strings.CutPrefix(name, "mcp__"); ok {
		// mcp__server__tool → keep only the tool segment
		parts := strings.SplitN(after, "__", 2)
		if len(parts) == 2 {
			name = parts[1]
		}
	}
	return name
}

func detectBashAnomaly(cmd string) (kind, detail string, found bool) {
	// Reverse shell / bind shell markers.
	for _, marker := range []string{
		"bash -i", "/dev/tcp/", "/dev/udp/",
		"mkfifo", "ncat ", "nc -e", "nc -lvp", "nc -lp",
		"python -c", "python3 -c", "perl -e", "ruby -rsocket",
		"socat ", "openssl s_client",
	} {
		if strings.Contains(cmd, marker) {
			return AnomalyReverseShell, "reverse shell pattern: " + marker, true
		}
	}

	// Outbound network calls that could exfiltrate data.
	for _, marker := range []string{"curl ", "wget ", "curl\t", "wget\t"} {
		if strings.Contains(cmd, marker) {
			return AnomalyNetworkEgress, "outbound network call: " + strings.TrimSpace(marker), true
		}
	}

	// Access to credential stores and sensitive system files.
	for _, path := range []string{
		"/.ssh/", "/.aws/", "/.config/gcloud", "/.gnupg/",
		"/etc/passwd", "/etc/shadow", "/etc/sudoers",
	} {
		if strings.Contains(cmd, path) {
			return AnomalyCredentialAccess, "sensitive path access: " + path, true
		}
	}

	return "", "", false
}
