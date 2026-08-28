package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/orlandoburli/apiary/internal/daemon"
)

// The manual workflow picker (Shift+W). It starts a chosen workflow immediately,
// skipping the trigger match and every pre-dispatch guard — including the one
// that normally prevents a second concurrent instance. `R` (restart) re-runs
// whatever matches an item; this runs the workflow you name, matched or not.

// noticeCmd returns a command that shows a one-line banner.
func noticeCmd(text string, isErr bool) tea.Cmd {
	return func() tea.Msg { return noticeMsg{text: text, isErr: isErr} }
}

// pickerWorkflows lists the workflow ids the picker offers. It prefers the tab's
// already-loaded config items (fetchWorkflowsConfig populates them at startup)
// and falls back to the live config, so the picker works before the Workflows
// tab has ever been visited.
func (a *App) pickerWorkflows() []string {
	if a.model.workflowsTab != nil && len(a.model.workflowsTab.Workflows) > 0 {
		ids := make([]string, 0, len(a.model.workflowsTab.Workflows))
		for _, wf := range a.model.workflowsTab.Workflows {
			if wf.ID != "" {
				ids = append(ids, wf.ID)
			}
		}
		return ids
	}
	if a.cfg == nil {
		return nil
	}
	ids := make([]string, 0, len(a.cfg.Workflows))
	for _, wf := range a.cfg.Workflows {
		if wf.ID != "" {
			ids = append(ids, wf.ID)
		}
	}
	return ids
}

// pickerTarget returns the item reference the run will bind, and whether the run
// is standalone (no source item).
func (a *App) pickerTarget() (ref string, standalone bool) {
	if a.model.pickerStandalone || a.model.pickerTaskID == "" {
		return "", true
	}
	return a.model.pickerTaskID, false
}

func (a *App) closeWorkflowPicker() {
	a.model.pickerActive = false
	a.model.pickerIdx = 0
	a.model.pickerTaskID = ""
	a.model.pickerStandalone = false
}

// handleWorkflowPickerKey consumes every key while the picker is open, the same
// way the confirm modal does — a half-open overlay that lets navigation keys
// through would move the selection underneath it.
func (a *App) handleWorkflowPickerKey(key string) (tea.Model, tea.Cmd) {
	ids := a.pickerWorkflows()
	if len(ids) == 0 {
		a.closeWorkflowPicker()
		return a, nil
	}
	if a.model.pickerIdx >= len(ids) {
		a.model.pickerIdx = len(ids) - 1
	}

	switch key {
	case "up", "k":
		if a.model.pickerIdx > 0 {
			a.model.pickerIdx--
		}
	case "down", "j":
		if a.model.pickerIdx < len(ids)-1 {
			a.model.pickerIdx++
		}
	case "home", "g":
		a.model.pickerIdx = 0
	case "end", "G":
		a.model.pickerIdx = len(ids) - 1
	case "s":
		// Toggle between the focused item and a standalone run. Only meaningful
		// when something is focused; with no focus the run is standalone anyway.
		if a.model.pickerTaskID != "" {
			a.model.pickerStandalone = !a.model.pickerStandalone
		}
	case "enter":
		workflowID := ids[a.model.pickerIdx]
		ref, _ := a.pickerTarget()
		a.closeWorkflowPicker()
		a.awaitManualRunInMonitor(ref)
		return a, a.runWorkflowCmd(workflowID, ref)
	case "esc":
		a.closeWorkflowPicker()
	}
	return a, nil
}

// wfMonitorAwaitTicks bounds how long the monitor keeps re-listing instances
// after a manual run (refreshInterval each), so a run the daemon accepted but
// never dispatched stops the polling instead of continuing for the whole session.
const wfMonitorAwaitTicks = 30

// awaitManualRunInMonitor arms the open workflow monitor to pick up the instance
// a manual run is about to create. Dispatch is asynchronous, so the instance does
// not exist yet; the monitor's refresh tick watches for it and switches to it.
// Without this the operator had to leave the screen and come back to see the run
// they just started.
func (a *App) awaitManualRunInMonitor(itemRef string) {
	t := a.model.tasksTab
	if t == nil || t.View != TaskViewWorkflow || itemRef == "" || t.WorkflowTaskID != itemRef {
		return
	}
	t.WorkflowAwaitTicks = wfMonitorAwaitTicks
}

// runWorkflowCmd asks the daemon to start a workflow manually and reports the
// outcome as a noticeMsg. Like restartTaskCmd, the daemon's own message is the
// whole diagnosis on failure and goes to the user verbatim.
func (a *App) runWorkflowCmd(workflowID, itemRef string) tea.Cmd {
	socketPath := a.socketPath
	return func() tea.Msg {
		transport := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
			},
		}
		client := &http.Client{Transport: transport, Timeout: 10 * time.Second}

		url := "http://apiary/workflows/" + neturl.PathEscape(workflowID) + "/run"
		if itemRef != "" {
			url += "?item=" + neturl.QueryEscape(itemRef)
		}
		resp, err := client.Post(url, "application/json", nil)
		if err != nil {
			return noticeMsg{text: fmt.Sprintf("Start %s failed: cannot reach daemon: %v", workflowID, err), isErr: true}
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode != http.StatusAccepted {
			msg := strings.TrimSpace(string(body))
			if msg == "" {
				msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
			}
			return noticeMsg{text: fmt.Sprintf("Start %s failed: %s", workflowID, msg), isErr: true}
		}

		var res daemon.ManualRunResult
		if err := json.Unmarshal(body, &res); err != nil {
			return noticeMsg{text: "Started workflow " + workflowID}
		}
		text := fmt.Sprintf("Started %s on %s (bypassed triggers and guards)", res.WorkflowID, res.Label())
		if res.Concurrent {
			text += " — it was already running, this is a second instance"
		}
		return noticeMsg{text: text}
	}
}

// renderWorkflowPicker overlays the workflow list, centred like the confirm
// modal. The header states the target up front: starting a workflow on the wrong
// item, or standalone when the user meant to bind one, is the mistake worth
// making unmissable.
func (a *App) renderWorkflowPicker(view string) string {
	ids := a.pickerWorkflows()
	ref, standalone := a.pickerTarget()

	target := "standalone — no source item"
	if !standalone {
		target = "item " + ref
	}

	// Cap the visible list so a long config cannot outgrow the screen; scroll it
	// around the selection.
	const maxRows = 12
	start := 0
	if len(ids) > maxRows && a.model.pickerIdx >= maxRows {
		start = a.model.pickerIdx - maxRows + 1
	}
	end := min(start+maxRows, len(ids))

	rows := make([]string, 0, maxRows+1)
	for i := start; i < end; i++ {
		line := "  " + ids[i]
		if i == a.model.pickerIdx {
			line = StyleFooterKey.Render(" ▸ " + ids[i] + " ")
		}
		rows = append(rows, line)
	}
	if end < len(ids) {
		rows = append(rows, StyleFooterDim.Render(fmt.Sprintf("  … %d more", len(ids)-end)))
	}

	toggle := ""
	if a.model.pickerTaskID != "" {
		toggle = "  " + StyleFooterKey.Render(" s ") + " " + StyleFooterLbl.Render("standalone")
	}

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorWarning).
		Padding(1, 3).
		Width(52).
		Render(
			lipgloss.JoinVertical(lipgloss.Left,
				StyleBoxTitle.Render(" Start a workflow "),
				StyleFooterDim.Render("target: "+target),
				StyleFooterDim.Render("runs now, ignoring triggers and guards"),
				"",
				strings.Join(rows, "\n"),
				"",
				StyleFooterKey.Render(" ↑↓ ")+" "+StyleFooterLbl.Render("select")+"  "+
					StyleFooterKey.Render(" ⏎ ")+" "+StyleFooterLbl.Render("start")+"  "+
					StyleFooterKey.Render(" esc ")+" "+StyleFooterLbl.Render("cancel")+toggle,
			),
		)

	topPad := (a.model.height - lipgloss.Height(dialog)) / 2
	if topPad < 0 {
		topPad = 0
	}
	return strings.Repeat("\n", topPad) +
		lipgloss.NewStyle().Width(a.model.width).Align(lipgloss.Center).Render(dialog)
}
