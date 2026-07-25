// Package audit records every tool call an agent makes and flags anomalies.
//
// The Auditor feeds on the same raw stream-json lines that the Transcript
// renderer consumes. Each tool_use block becomes one agent.action execution
// event; anomalies (suspicious commands, sensitive file access, unexpected
// network egress) emit an additional agent.anomaly event and invoke the
// configured AlertHandler so operators are notified in real time.
package audit

import (
	"context"
	"encoding/json"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/orlandoburli/apiary/internal/db"
)

// ActionKind classifies what an agent tool call did.
type ActionKind string

const (
	ActionCommand      ActionKind = "command"       // shell/bash execution
	ActionFileRead     ActionKind = "file_read"     // file read
	ActionFileWrite    ActionKind = "file_write"    // file write or edit
	ActionNetworkEgress ActionKind = "network_egress" // HTTP request / web fetch
	ActionOther        ActionKind = "other"         // unclassified tool call
)

// Action is one audited tool call.
type Action struct {
	Tool    string     // tool name as reported by the runner (e.g. "Bash")
	Kind    ActionKind
	Detail  string // command text, file path, or URL
	RawJSON string // raw input JSON for forensics
}

// AnomalyRule identifies which check fired.
type AnomalyRule string

const (
	RuleSuspiciousCommand  AnomalyRule = "suspicious_command"
	RuleSensitiveFileRead  AnomalyRule = "sensitive_file_read"
	RuleSensitiveFileWrite AnomalyRule = "sensitive_file_write"
	RuleUnexpectedEgress   AnomalyRule = "unexpected_egress"
)

// Severity indicates how urgent an anomaly is.
type Severity string

const (
	SeverityHigh   Severity = "high"
	SeverityMedium Severity = "medium"
	SeverityLow    Severity = "low"
)

// Anomaly is a detected suspicious action.
type Anomaly struct {
	Rule     AnomalyRule
	Severity Severity
	Detail   string
	Action   Action
}

// AlertHandler is called for each anomaly detected during a run.
// Implementations must be safe to call from any goroutine.
type AlertHandler func(ctx context.Context, anomaly Anomaly, taskID, instanceID, stepID string)

// EventRecorder persists execution events. db.Client satisfies this.
type EventRecorder interface {
	RecordExecutionEvent(ctx context.Context, event *db.ExecutionEvent) error
}

// Config controls audit behaviour.
type Config struct {
	// Enabled turns the auditor on or off. When false Feed is a no-op.
	Enabled bool
	// AllowedEgressDomains, when non-empty, makes any network egress to an
	// unlisted domain an anomaly (RuleUnexpectedEgress).
	AllowedEgressDomains []string
}

// Auditor watches an agent's stream-json output, records every tool call as an
// agent.action execution event, and raises anomaly alerts when suspicious
// patterns are detected.
type Auditor struct {
	cfg        Config
	db         EventRecorder
	alert      AlertHandler
	taskID     string
	instanceID string
	stepID     string

	// normalised allow-list for fast O(1) look-up
	allowedDomains map[string]struct{}
}

// New creates an Auditor for one step run.
// db and alert may be nil — event recording and alerts are best-effort.
func New(cfg Config, recorder EventRecorder, alert AlertHandler, taskID, instanceID, stepID string) *Auditor {
	a := &Auditor{
		cfg:        cfg,
		db:         recorder,
		alert:      alert,
		taskID:     taskID,
		instanceID: instanceID,
		stepID:     stepID,
	}
	if len(cfg.AllowedEgressDomains) > 0 {
		a.allowedDomains = make(map[string]struct{}, len(cfg.AllowedEgressDomains))
		for _, d := range cfg.AllowedEgressDomains {
			a.allowedDomains[normalizeHost(d)] = struct{}{}
		}
	}
	return a
}

// Feed processes one raw stdout line from the runner. It is safe to call from
// multiple goroutines. When the auditor is disabled it returns immediately.
func (a *Auditor) Feed(rawLine string) {
	if !a.cfg.Enabled {
		return
	}
	trimmed := strings.TrimSpace(rawLine)
	if !strings.HasPrefix(trimmed, "{") {
		return
	}
	actions := extractActions(trimmed)
	if len(actions) == 0 {
		return
	}
	ctx := context.Background()
	for _, action := range actions {
		a.record(ctx, action)
		a.detect(ctx, action)
	}
}

// record persists one agent.action event.
func (a *Auditor) record(ctx context.Context, action Action) {
	if a.db == nil {
		return
	}
	meta := map[string]any{
		"tool": action.Tool,
		"kind": string(action.Kind),
	}
	switch action.Kind {
	case ActionCommand:
		meta["command"] = action.Detail
	case ActionFileRead, ActionFileWrite:
		meta["path"] = action.Detail
	case ActionNetworkEgress:
		meta["url"] = action.Detail
	default:
		if action.Detail != "" {
			meta["detail"] = action.Detail
		}
	}
	ev := &db.ExecutionEvent{
		Type:               "agent.action",
		Timestamp:          time.Now().UTC(),
		TaskID:             a.taskID,
		WorkflowInstanceID: a.instanceID,
		StepID:             a.stepID,
		Metadata:           meta,
	}
	// best-effort; failures are silently dropped so the run is never blocked
	_ = a.db.RecordExecutionEvent(ctx, ev)
}

// detect runs anomaly checks on a single action.
func (a *Auditor) detect(ctx context.Context, action Action) {
	var anomalies []Anomaly

	switch action.Kind {
	case ActionCommand:
		if rule, sev, detail := checkCommand(action.Detail); rule != "" {
			anomalies = append(anomalies, Anomaly{Rule: rule, Severity: sev, Detail: detail, Action: action})
		}
	case ActionFileRead:
		if isSensitivePath(action.Detail) {
			anomalies = append(anomalies, Anomaly{
				Rule:     RuleSensitiveFileRead,
				Severity: SeverityMedium,
				Detail:   "agent read sensitive path: " + action.Detail,
				Action:   action,
			})
		}
	case ActionFileWrite:
		if isSensitivePath(action.Detail) {
			anomalies = append(anomalies, Anomaly{
				Rule:     RuleSensitiveFileWrite,
				Severity: SeverityHigh,
				Detail:   "agent wrote to sensitive path: " + action.Detail,
				Action:   action,
			})
		}
	case ActionNetworkEgress:
		if a.allowedDomains != nil {
			host := normalizeHost(hostFromURL(action.Detail))
			if host != "" {
				if _, ok := a.allowedDomains[host]; !ok {
					anomalies = append(anomalies, Anomaly{
						Rule:     RuleUnexpectedEgress,
						Severity: SeverityMedium,
						Detail:   "network egress to unlisted domain: " + host,
						Action:   action,
					})
				}
			}
		}
	}

	for _, an := range anomalies {
		a.raiseAnomaly(ctx, an)
	}
}

// raiseAnomaly records an agent.anomaly event and fires the alert handler.
func (a *Auditor) raiseAnomaly(ctx context.Context, an Anomaly) {
	if a.db != nil {
		meta := map[string]any{
			"rule":        string(an.Rule),
			"severity":    string(an.Severity),
			"detail":      an.Detail,
			"tool":        an.Action.Tool,
			"action_kind": string(an.Action.Kind),
		}
		if an.Action.Detail != "" {
			meta["action_detail"] = an.Action.Detail
		}
		ev := &db.ExecutionEvent{
			Type:               "agent.anomaly",
			Timestamp:          time.Now().UTC(),
			TaskID:             a.taskID,
			WorkflowInstanceID: a.instanceID,
			StepID:             a.stepID,
			Metadata:           meta,
		}
		_ = a.db.RecordExecutionEvent(ctx, ev)
	}
	if a.alert != nil {
		a.alert(ctx, an, a.taskID, a.instanceID, a.stepID)
	}
}

// ── stream-json parsing ───────────────────────────────────────────────────────

// streamMsg mirrors only the fields the auditor needs from the Claude CLI's
// stream-json "assistant" events.
type streamMsg struct {
	Type    string `json:"type"`
	Message struct {
		Content []struct {
			Type  string          `json:"type"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
}

// extractActions parses a single raw JSON line and returns any tool_use blocks.
func extractActions(line string) []Action {
	var msg streamMsg
	if err := json.Unmarshal([]byte(line), &msg); err != nil || msg.Type != "assistant" {
		return nil
	}
	var actions []Action
	for _, c := range msg.Message.Content {
		if c.Type != "tool_use" {
			continue
		}
		action := classifyToolUse(c.Name, c.Input)
		actions = append(actions, action)
	}
	return actions
}

// classifyToolUse maps a tool name and its raw input JSON to an Action.
func classifyToolUse(name string, rawInput json.RawMessage) Action {
	var input map[string]json.RawMessage
	_ = json.Unmarshal(rawInput, &input)

	getString := func(key string) string {
		raw, ok := input[key]
		if !ok {
			return ""
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return strings.Trim(string(raw), `"`)
		}
		return s
	}

	rawStr := string(rawInput)
	lower := strings.ToLower(name)

	switch {
	case lower == "bash" || lower == "computer" || strings.Contains(lower, "bash") || strings.Contains(lower, "shell"):
		cmd := getString("command")
		if cmd == "" {
			cmd = getString("cmd")
		}
		return Action{Tool: name, Kind: ActionCommand, Detail: cmd, RawJSON: rawStr}

	case lower == "read" || lower == "cat" || lower == "view" || lower == "readfile":
		p := getString("file_path")
		if p == "" {
			p = getString("path")
		}
		return Action{Tool: name, Kind: ActionFileRead, Detail: p, RawJSON: rawStr}

	case lower == "write" || lower == "writefile":
		p := getString("file_path")
		if p == "" {
			p = getString("path")
		}
		return Action{Tool: name, Kind: ActionFileWrite, Detail: p, RawJSON: rawStr}

	case lower == "edit" || lower == "multiedit" || lower == "str_replace_editor" ||
		lower == "str_replace_based_edit_tool" || strings.Contains(lower, "edit"):
		p := getString("file_path")
		if p == "" {
			p = getString("path")
		}
		return Action{Tool: name, Kind: ActionFileWrite, Detail: p, RawJSON: rawStr}

	case lower == "webfetch" || lower == "fetch" || lower == "httpfetch" ||
		strings.Contains(lower, "fetch") || strings.Contains(lower, "curl") ||
		strings.Contains(lower, "http"):
		u := getString("url")
		return Action{Tool: name, Kind: ActionNetworkEgress, Detail: u, RawJSON: rawStr}

	case lower == "websearch" || strings.Contains(lower, "search"):
		// websearch does not directly egress to a user-controlled URL; emit as
		// network egress with the query as the detail.
		q := getString("query")
		if q == "" {
			q = getString("q")
		}
		return Action{Tool: name, Kind: ActionNetworkEgress, Detail: q, RawJSON: rawStr}

	default:
		return Action{Tool: name, Kind: ActionOther, RawJSON: rawStr}
	}
}

// ── anomaly detection ─────────────────────────────────────────────────────────

// suspiciousCommandPatterns captures common post-exploitation patterns:
// piping downloaded content directly to a shell.
var suspiciousCommandPatterns = []*regexp.Regexp{
	// curl/wget piped to sh/bash/exec
	regexp.MustCompile(`(?i)(curl|wget)\s[^|]*\|\s*(ba?sh|sh|exec|python\d?|perl|ruby|node)`),
	// base64 decode piped to shell (optional filename arg before the pipe)
	regexp.MustCompile(`(?i)base64\s+(-d|--decode)\b[^|]*\|`),
	// eval of subshell that downloads content
	regexp.MustCompile(`(?i)eval\s*\$\((curl|wget)`),
	// direct exec() with network-fetched content
	regexp.MustCompile(`(?i)(python|perl|ruby|node)\s+-c\s+.*?(curl|wget|urllib|requests)`),
}

func checkCommand(cmd string) (AnomalyRule, Severity, string) {
	if cmd == "" {
		return "", "", ""
	}
	for _, re := range suspiciousCommandPatterns {
		if re.MatchString(cmd) {
			return RuleSuspiciousCommand, SeverityHigh,
				"command pipes network content to shell: " + truncate(cmd, 200)
		}
	}
	return "", "", ""
}

// sensitivePaths are glob-style prefix/suffix patterns for sensitive file paths.
var sensitivePaths = []string{
	".ssh/",
	"id_rsa",
	"id_ed25519",
	"id_ecdsa",
	"id_dsa",
	"authorized_keys",
	"known_hosts",
	".gnupg/",
	".gpg",
	"/.netrc",
	".env",
	".envrc",
	"credentials",
	"/etc/shadow",
	"/etc/passwd",
	"/etc/sudoers",
	"aws/credentials",
	".npmrc",
	".pypirc",
	".docker/config",
}

func isSensitivePath(p string) bool {
	if p == "" {
		return false
	}
	clean := path.Clean(p)
	lower := strings.ToLower(clean)
	for _, pat := range sensitivePaths {
		if strings.Contains(lower, strings.ToLower(pat)) {
			return true
		}
	}
	return false
}

// ── helpers ───────────────────────────────────────────────────────────────────

func hostFromURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	// Handle bare domain references (no scheme).
	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func normalizeHost(host string) string {
	// Strip leading "www." so that www.github.com matches github.com.
	host = strings.ToLower(strings.TrimSpace(host))
	return strings.TrimPrefix(host, "www.")
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
