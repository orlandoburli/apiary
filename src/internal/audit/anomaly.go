// Package audit provides anomaly detection for agent tool calls.
// It classifies each tool invocation against a set of rules that flag patterns
// associated with prompt injection (exfiltration, credential access, persistence).
package audit

import (
	"encoding/json"
	"strings"
)

// Flag is a named anomaly rule that fired on a tool call.
type Flag string

const (
	// FlagExfiltration: outbound network call to an unexpected host.
	FlagExfiltration Flag = "exfiltration"
	// FlagCredentialAccess: read or write of credential/key files.
	FlagCredentialAccess Flag = "credential_access"
	// FlagPersistence: write to a file that survives reboots (cron, init, profile).
	FlagPersistence Flag = "persistence"
	// FlagPrivilegeEscalation: setuid/setgid or sudo manipulation.
	FlagPrivilegeEscalation Flag = "privilege_escalation"
	// FlagMassDeletion: destructive rm -rf of a broad path.
	FlagMassDeletion Flag = "mass_deletion"
	// FlagObfuscatedExecution: base64-decode-and-execute or eval of dynamic content.
	FlagObfuscatedExecution Flag = "obfuscated_execution"
	// FlagSensitivePathWrite: write to /etc/ or system configuration paths.
	FlagSensitivePathWrite Flag = "sensitive_path_write"
)

// Check classifies a single agent tool call and returns the anomaly flags that
// fired. An empty slice means the tool call looks benign. toolName is the
// provider's tool identifier (e.g. "bash", "str_replace_editor", "computer");
// inputJSON is the raw JSON object the agent supplied as the tool's input.
func Check(toolName, inputJSON string) []Flag {
	var input map[string]any
	_ = json.Unmarshal([]byte(inputJSON), &input)

	var flags []Flag
	lower := strings.ToLower(toolName)

	switch {
	case isShellTool(lower):
		flags = append(flags, checkShellInput(input)...)
	case isFileTool(lower):
		flags = append(flags, checkFileInput(input)...)
	case isNetworkTool(lower):
		flags = append(flags, checkNetworkInput(input)...)
	default:
		// For unknown tools, scan all string values for high-signal patterns.
		flags = append(flags, checkGenericInput(input)...)
	}
	return dedup(flags)
}

// isShellTool returns true for tool names that execute shell commands.
func isShellTool(name string) bool {
	return name == "bash" || name == "shell" || name == "run_command" ||
		name == "execute_command" || strings.Contains(name, "bash") ||
		strings.Contains(name, "shell") || strings.Contains(name, "terminal")
}

// isFileTool returns true for tool names that write/edit files.
func isFileTool(name string) bool {
	return strings.Contains(name, "write") || strings.Contains(name, "edit") ||
		strings.Contains(name, "create") || strings.Contains(name, "str_replace") ||
		name == "notebook_edit"
}

// isNetworkTool returns true for tool names that make HTTP/network requests.
func isNetworkTool(name string) bool {
	return strings.Contains(name, "fetch") || strings.Contains(name, "http") ||
		strings.Contains(name, "web") || strings.Contains(name, "request") ||
		name == "curl"
}

func checkShellInput(input map[string]any) []Flag {
	cmd := extractString(input, "command", "cmd", "script", "code")
	if cmd == "" {
		return nil
	}
	lower := strings.ToLower(cmd)
	var flags []Flag

	if hasExfiltration(lower) {
		flags = append(flags, FlagExfiltration)
	}
	if hasCredentialAccess(lower) {
		flags = append(flags, FlagCredentialAccess)
	}
	if hasPersistence(lower) {
		flags = append(flags, FlagPersistence)
	}
	if hasPrivilegeEscalation(lower) {
		flags = append(flags, FlagPrivilegeEscalation)
	}
	if hasMassDeletion(lower) {
		flags = append(flags, FlagMassDeletion)
	}
	if hasObfuscatedExecution(lower) {
		flags = append(flags, FlagObfuscatedExecution)
	}
	if hasSensitivePathWrite(lower) {
		flags = append(flags, FlagSensitivePathWrite)
	}
	return flags
}

func checkFileInput(input map[string]any) []Flag {
	path := extractString(input, "path", "file_path", "filename", "file", "new_path")
	if path == "" {
		return nil
	}
	lower := strings.ToLower(path)
	var flags []Flag

	if matchesSensitivePath(lower) {
		flags = append(flags, FlagSensitivePathWrite)
	}
	if matchesCredentialPath(lower) {
		flags = append(flags, FlagCredentialAccess)
	}
	if matchesPersistencePath(lower) {
		flags = append(flags, FlagPersistence)
	}
	return flags
}

func checkNetworkInput(input map[string]any) []Flag {
	url := extractString(input, "url", "uri", "endpoint", "href")
	if url == "" {
		return nil
	}
	if isExternalURL(url) {
		return []Flag{FlagExfiltration}
	}
	return nil
}

func checkGenericInput(input map[string]any) []Flag {
	// Walk all string values and apply shell + path checks.
	var flags []Flag
	for _, v := range input {
		if s, ok := v.(string); ok {
			lower := strings.ToLower(s)
			if hasExfiltration(lower) {
				flags = append(flags, FlagExfiltration)
			}
			if hasObfuscatedExecution(lower) {
				flags = append(flags, FlagObfuscatedExecution)
			}
		}
	}
	return flags
}

// hasExfiltration detects outbound data exfiltration patterns in shell commands.
func hasExfiltration(cmd string) bool {
	// curl/wget/nc sending data to a non-loopback host
	for _, prog := range []string{"curl ", "wget ", "nc ", "ncat ", "netcat "} {
		if !strings.Contains(cmd, prog) {
			continue
		}
		// Allow localhost / loopback
		if strings.Contains(cmd, "localhost") || strings.Contains(cmd, "127.0.0.1") ||
			strings.Contains(cmd, "::1") || strings.Contains(cmd, "0.0.0.0") {
			continue
		}
		// curl/wget with a -d / --data flag or piped content is upload
		if strings.Contains(cmd, " -d ") || strings.Contains(cmd, "--data") ||
			strings.Contains(cmd, " -F ") || strings.Contains(cmd, " -T ") {
			return true
		}
		// Any curl/wget/nc to an external host that isn't purely a GET for a known tool
		if !isKnownSafeHost(cmd) {
			return true
		}
	}
	// ssh/scp/sftp to external
	if (strings.Contains(cmd, "ssh ") || strings.Contains(cmd, "scp ") ||
		strings.Contains(cmd, "sftp ")) &&
		!strings.Contains(cmd, "localhost") && !strings.Contains(cmd, "127.0.0.1") {
		return true
	}
	return false
}

// isKnownSafeHost returns true when a command targets only well-known public
// package registries or CDNs. The list is intentionally narrow — unknown hosts
// are flagged and let the operator decide.
func isKnownSafeHost(cmd string) bool {
	safeHosts := []string{
		"pkg.go.dev", "proxy.golang.org", "sum.golang.org",
		"registry.npmjs.org", "npmjs.com",
		"pypi.org", "files.pythonhosted.org",
		"crates.io", "static.crates.io",
		"api.github.com", "github.com", "raw.githubusercontent.com",
		"objects.githubusercontent.com",
		"dl.google.com", "storage.googleapis.com",
		"packages.microsoft.com",
		"deb.debian.org", "security.debian.org",
		"archive.ubuntu.com", "security.ubuntu.com",
		"homebrew.bintray.com", "formulae.brew.sh",
		"releases.hashicorp.com",
	}
	for _, h := range safeHosts {
		if strings.Contains(cmd, h) {
			return true
		}
	}
	return false
}

func hasCredentialAccess(cmd string) bool {
	sensitiveFiles := []string{
		".ssh/id_rsa", ".ssh/id_ed25519", ".ssh/id_ecdsa",
		".ssh/authorized_keys", ".ssh/known_hosts",
		".aws/credentials", ".aws/config",
		".gnupg/", ".gpg",
		"/etc/passwd", "/etc/shadow", "/etc/sudoers",
		"~/.netrc", "/.netrc",
		"keychain", "keystore",
	}
	for _, f := range sensitiveFiles {
		if strings.Contains(cmd, f) {
			return true
		}
	}
	return false
}

func hasPersistence(cmd string) bool {
	persistencePatterns := []string{
		"crontab ", "/etc/cron", "/var/spool/cron",
		"/etc/rc.", "/etc/init.d/", "/etc/systemd/", "/lib/systemd/",
		"~/.bashrc", "~/.zshrc", "~/.profile", "~/.bash_profile",
		"~/.config/autostart",
		"/etc/profile", "/etc/bash.bashrc",
		"launchctl ", "launchd", "/Library/LaunchDaemons/", "/Library/LaunchAgents/",
	}
	for _, p := range persistencePatterns {
		if strings.Contains(cmd, p) {
			return true
		}
	}
	return false
}

func hasPrivilegeEscalation(cmd string) bool {
	return strings.Contains(cmd, "chmod +s") ||
		strings.Contains(cmd, "chmod u+s") ||
		strings.Contains(cmd, "chmod g+s") ||
		strings.Contains(cmd, "chown root") ||
		strings.Contains(cmd, "sudo -i") ||
		strings.Contains(cmd, "sudo su") ||
		strings.Contains(cmd, "visudo") ||
		strings.Contains(cmd, "/etc/sudoers")
}

func hasMassDeletion(cmd string) bool {
	// rm -rf on broad paths: /, /home, /etc, /usr, etc.
	if !strings.Contains(cmd, "rm ") {
		return false
	}
	broadPaths := []string{
		"rm -rf /", "rm -rf ~/", "rm -rf $home",
		"rm -rf /home", "rm -rf /etc", "rm -rf /usr",
		"rm -rf /var", "rm -rf /tmp/*", "rm -rf /root",
		"rm -r /", "rm -rf /*",
	}
	for _, p := range broadPaths {
		if strings.Contains(cmd, p) {
			return true
		}
	}
	return false
}

func hasObfuscatedExecution(cmd string) bool {
	// base64 decode piped to shell
	if (strings.Contains(cmd, "base64") && strings.Contains(cmd, "decode") &&
		(strings.Contains(cmd, "| sh") || strings.Contains(cmd, "| bash") ||
			strings.Contains(cmd, "|sh") || strings.Contains(cmd, "|bash"))) {
		return true
	}
	// echo | base64 -d | sh
	if strings.Contains(cmd, "base64 -d") &&
		(strings.Contains(cmd, "| sh") || strings.Contains(cmd, "| bash")) {
		return true
	}
	// eval of dynamic content
	if strings.Contains(cmd, "eval $(") || strings.Contains(cmd, "eval \"$(") ||
		strings.Contains(cmd, "eval `") {
		return true
	}
	// $() subshell capturing curl output and executing it
	if (strings.Contains(cmd, "$(curl") || strings.Contains(cmd, "$(wget")) &&
		(strings.Contains(cmd, "| sh") || strings.Contains(cmd, "| bash") ||
			strings.Contains(cmd, "|sh") || strings.Contains(cmd, "|bash")) {
		return true
	}
	return false
}

func hasSensitivePathWrite(cmd string) bool {
	// Redirect (>) into /etc/ or similar
	if !strings.Contains(cmd, ">") && !strings.Contains(cmd, "tee ") {
		return false
	}
	return matchesSensitivePath(cmd)
}

func matchesSensitivePath(path string) bool {
	sensitivePaths := []string{
		"/etc/passwd", "/etc/shadow", "/etc/sudoers",
		"/etc/hosts", "/etc/hostname", "/etc/resolv.conf",
		"/etc/ssh/", "/etc/cron",
	}
	for _, p := range sensitivePaths {
		if strings.Contains(path, p) {
			return true
		}
	}
	return false
}

func matchesCredentialPath(path string) bool {
	credPaths := []string{
		".ssh/authorized_keys", ".ssh/id_rsa", ".ssh/id_ed25519",
		".aws/credentials", ".gnupg/", ".netrc",
		"/etc/passwd", "/etc/shadow",
	}
	for _, p := range credPaths {
		if strings.Contains(path, p) {
			return true
		}
	}
	return false
}

func matchesPersistencePath(path string) bool {
	persistPaths := []string{
		".bashrc", ".zshrc", ".profile", ".bash_profile",
		"/etc/cron", "/var/spool/cron",
		"/etc/rc.", "/etc/init.d/", "/etc/systemd/",
		"/Library/LaunchDaemons/", "/Library/LaunchAgents/",
	}
	for _, p := range persistPaths {
		if strings.Contains(path, p) {
			return true
		}
	}
	return false
}

// isExternalURL returns true for URLs that point outside localhost.
func isExternalURL(url string) bool {
	lower := strings.ToLower(url)
	// Relative URLs and loopback are safe
	if !strings.HasPrefix(lower, "http") {
		return false
	}
	safeHosts := []string{
		"localhost", "127.0.0.1", "::1", "0.0.0.0",
		"api.github.com", "github.com", "raw.githubusercontent.com",
	}
	for _, h := range safeHosts {
		if strings.Contains(lower, h) {
			return false
		}
	}
	return true
}

// extractString returns the first non-empty string value for any of the given
// keys from the input map.
func extractString(input map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := input[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

func dedup(flags []Flag) []Flag {
	seen := make(map[Flag]struct{}, len(flags))
	out := flags[:0]
	for _, f := range flags {
		if _, ok := seen[f]; !ok {
			seen[f] = struct{}{}
			out = append(out, f)
		}
	}
	return out
}
