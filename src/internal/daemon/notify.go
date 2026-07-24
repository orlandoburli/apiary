package daemon

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/orlandoburli/apiary/internal/audit"
	"github.com/orlandoburli/apiary/internal/config"
	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/model"
)

// notifyTimeout bounds each notification command so a hung hook (network
// webhook, stuck script) can never pile up goroutines.
const notifyTimeout = 60 * time.Second

// escalationEvent carries everything a notification channel may reference.
type escalationEvent struct {
	TaskID  string
	CellID  string
	Number  string
	Title   string
	URL     string
	Label   string
	Summary string
}

// matchedEscalationLabels returns the hook labels that are configured as
// escalation labels, preserving hook order.
func matchedEscalationLabels(cfg *config.NotificationsConfig, added []string) []string {
	if cfg == nil || len(cfg.Channels) == 0 {
		return nil
	}
	watch := make(map[string]bool, len(cfg.OnLabels))
	for _, l := range cfg.OnLabels {
		watch[l] = true
	}
	var out []string
	for _, l := range added {
		if watch[l] {
			out = append(out, l)
		}
	}
	return out
}

// notifyEscalation fires every configured channel for one escalation label,
// asynchronously — notification is observability, it must never block or fail
// a hook. Errors are logged and swallowed.
func (d *Dispatcher) notifyEscalation(ev escalationEvent) {
	n := d.cfg.Notifications
	if n == nil {
		return
	}
	for i, ch := range n.Channels {
		if ch.Type != "command" || strings.TrimSpace(ch.Run) == "" {
			continue
		}
		idx, run := i, ch.Run
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
			defer cancel()
			cmdline := renderNotifyCommand(run, ev)
			cmd := exec.CommandContext(ctx, "sh", "-c", cmdline)
			cmd.Env = append(os.Environ(),
				"APIARY_TASK_ID="+ev.TaskID,
				"APIARY_CELL_ID="+ev.CellID,
				"APIARY_NUMBER="+ev.Number,
				"APIARY_TITLE="+ev.Title,
				"APIARY_URL="+ev.URL,
				"APIARY_LABEL="+ev.Label,
				"APIARY_SUMMARY="+ev.Summary,
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				aplog.Error("notification channel %d for %s (label %q): %v — %s",
					idx, ev.CellID, ev.Label, err, strings.TrimSpace(string(out)))
				return
			}
			aplog.Info("notification sent: channel %d for %s (label %q)", idx, ev.CellID, ev.Label)
		}()
	}
}

// renderNotifyCommand substitutes {{placeholder}} tokens with shell-quoted
// values, so titles/summaries with spaces or quotes can never break (or
// inject into) the command line. Operators who prefer raw values can use the
// APIARY_* environment variables instead.
func renderNotifyCommand(tmpl string, ev escalationEvent) string {
	r := strings.NewReplacer(
		"{{task_id}}", shellQuote(ev.TaskID),
		"{{cell_id}}", shellQuote(ev.CellID),
		"{{number}}", shellQuote(ev.Number),
		"{{title}}", shellQuote(ev.Title),
		"{{url}}", shellQuote(ev.URL),
		"{{label}}", shellQuote(ev.Label),
		"{{summary}}", shellQuote(ev.Summary),
	)
	return r.Replace(tmpl)
}

// shellQuote wraps s in single quotes, escaping embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// escalationSummary digs up the most useful one-liner for the event: the last
// non-empty step summary of the task's newest workflow instance, falling back
// to the last failed step's error, then to the task title. Best-effort — any
// DB error just falls back.
func (d *Dispatcher) escalationSummary(ctx context.Context, task model.InternalTask) string {
	if d.db == nil {
		return task.Title
	}
	instances, err := d.db.ListWorkflowInstancesByTask(ctx, task.ID)
	if err != nil || len(instances) == 0 {
		return task.Title
	}
	// Newest first is not guaranteed by the store; scan for the latest.
	latest := instances[0]
	for _, in := range instances[1:] {
		if in.CreatedAt.After(latest.CreatedAt) {
			latest = in
		}
	}
	steps, err := d.db.ListStepRuns(ctx, latest.ID)
	if err != nil {
		return task.Title
	}
	for i := len(steps) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(steps[i].Summary); s != "" {
			return s
		}
	}
	for i := len(steps) - 1; i >= 0; i-- {
		if steps[i].State == "failed" && strings.TrimSpace(steps[i].Output) != "" {
			return firstLine(steps[i].Output)
		}
	}
	return task.Title
}

// anomalyEvent carries everything a security anomaly notification may reference.
type anomalyEvent struct {
	TaskID             string
	CellID             string
	WorkflowInstanceID string
	StepID             string
	ToolName           string
	Flags              []audit.Flag
}

// notifyAnomaly fires every configured notification channel when an anomalous
// agent action is detected. It reuses the same channels as label escalations so
// operators only need one notification config. Like notifyEscalation it is
// asynchronous and errors are logged-and-swallowed — alerts must never block
// agent execution.
func (d *Dispatcher) notifyAnomaly(ev anomalyEvent) {
	n := d.cfg.Notifications
	if n == nil || len(n.Channels) == 0 {
		return
	}
	flagStrs := make([]string, len(ev.Flags))
	for i, f := range ev.Flags {
		flagStrs[i] = string(f)
	}
	flagSummary := strings.Join(flagStrs, ",")
	for i, ch := range n.Channels {
		if ch.Type != "command" || strings.TrimSpace(ch.Run) == "" {
			continue
		}
		idx, run := i, ch.Run
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
			defer cancel()
			// Reuse the same shell-quoting template substitution as escalation
			// notifications, but with security-specific placeholders.
			cmdline := renderNotifyCommand(run, escalationEvent{
				TaskID:  ev.TaskID,
				CellID:  ev.CellID,
				Label:   "security.anomaly",
				Summary: "anomalous tool call: " + ev.ToolName + " flags=" + flagSummary,
			})
			cmd := exec.CommandContext(ctx, "sh", "-c", cmdline)
			cmd.Env = append(os.Environ(),
				"APIARY_TASK_ID="+ev.TaskID,
				"APIARY_CELL_ID="+ev.CellID,
				"APIARY_WORKFLOW_INSTANCE_ID="+ev.WorkflowInstanceID,
				"APIARY_STEP_ID="+ev.StepID,
				"APIARY_TOOL_NAME="+ev.ToolName,
				"APIARY_ANOMALY_FLAGS="+flagSummary,
				"APIARY_LABEL=security.anomaly",
				"APIARY_SUMMARY="+"anomalous tool call: "+ev.ToolName+" flags="+flagSummary,
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				aplog.Error("anomaly notification channel %d for %s (tool %q flags %s): %v — %s",
					idx, ev.CellID, ev.ToolName, flagSummary, err, strings.TrimSpace(string(out)))
				return
			}
			aplog.Info("anomaly notification sent: channel %d for %s (tool %q flags %s)",
				idx, ev.CellID, ev.ToolName, flagSummary)
		}()
	}
}

// firstLine returns the first non-empty line of s, capped for notification use.
func firstLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		const max = 300
		if len(line) > max {
			return line[:max] + "…"
		}
		return line
	}
	return ""
}
