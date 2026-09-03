package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/daemon"
	"github.com/orlandoburli/apiary/internal/format"
	apstate "github.com/orlandoburli/apiary/internal/state"
)

var (
	instHeader = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#DDDDDD"))
	instMuted  = lipgloss.NewStyle().Foreground(lipgloss.Color("#626262"))
	instOK     = lipgloss.NewStyle().Foreground(lipgloss.Color("#04B575"))
	instWarn   = lipgloss.NewStyle().Foreground(lipgloss.Color("#F5A623"))
	instErr    = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5F87"))
)

func newInstancesCmd() *cobra.Command {
	var (
		workflow    string
		state       string
		limit       int
		asJSON      bool
		cancel      bool
		ticketsOnly bool
	)

	cmd := &cobra.Command{
		Use:   "instances [instance-id]",
		Short: "List workflow instances, show one in detail, or cancel one",
		Long: "List workflow instances, show one in detail, or cancel one.\n\n" +
			"--cancel stops a single running instance: its in-flight step is cancelled\n" +
			"and the instance is marked interrupted, without touching the source item's\n" +
			"labels or state (unlike `apiary restart`, which acts on the whole cell).\n" +
			"Queued or leased dispatch jobs for the same task and workflow are cancelled\n" +
			"with it. A cancelled instance can be continued later with `apiary resume`.\n\n" +
			"--tickets-only hides instances with no real ticket behind them — scheduled\n" +
			"routine runs and other plugin-sourced work items — keeping only instances\n" +
			"bound to a source item from a ticket-tracker source (jira, github, plane, ...).",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cancel {
				if len(args) != 1 {
					return fmt.Errorf("--cancel needs an instance id: apiary instances <instance-id> --cancel")
				}
				return cancelInstance(args[0], asJSON)
			}
			if len(args) == 1 {
				return showInstance(args[0], asJSON)
			}
			return listInstances(workflow, state, limit, asJSON, ticketsOnly)
		},
	}

	cmd.Flags().StringVar(&workflow, "workflow", "", "filter by workflow id")
	cmd.Flags().StringVar(&state, "state", "", "filter by state (pending, running, approval_waiting, interrupted, done, failed)")
	cmd.Flags().IntVar(&limit, "limit", 20, "max rows")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output as JSON")
	cmd.Flags().BoolVar(&cancel, "cancel", false, "stop this instance and mark it interrupted")
	cmd.Flags().BoolVar(&ticketsOnly, "tickets-only", false,
		"only show instances bound to a real ticket/issue (excludes scheduled routine and other plugin-sourced runs)")
	cmd.AddCommand(newInstancesCompareCmd())
	return cmd
}

func newInstancesCompareCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "compare <before-id> <after-id>",
		Short: "Compare two workflow attempts step by step",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return compareInstances(args[0], args[1], asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output as JSON")
	return cmd
}

func compareInstances(beforeID, afterID string, asJSON bool) error {
	q := url.Values{"before": {beforeID}, "after": {afterID}}
	var comparison daemon.InstanceComparison
	if err := ipcGetJSON("/instances/compare?"+q.Encode(), &comparison); err != nil {
		return daemonDownHint()
	}
	if asJSON {
		line, _ := json.MarshalIndent(comparison, "", "  ")
		fmt.Println(string(line))
		return nil
	}
	fmt.Printf("Comparing %s → %s\n\n", comparison.BeforeID, comparison.AfterID)
	fmt.Println(instHeader.Render(fmt.Sprintf("%-18s %-13s %-13s %-10s %-10s %-12s %-10s", "STEP", "BEFORE", "AFTER", "INPUT", "OUTPUT", "USAGE Δ", "TIME Δ")))
	for _, row := range comparison.Steps {
		before, after := "—", "—"
		if row.Before != nil {
			before = row.Before.State
		}
		if row.After != nil {
			after = row.After.State
		}
		input, output := "same", "same"
		if row.InputChanged {
			input = "changed"
		}
		if row.OutputChanged {
			output = "changed"
		}
		usage := fmt.Sprintf("%s / %s", format.TokensDelta(row.TokenDelta), format.USDDelta(row.CostDeltaUSD))
		timing := fmt.Sprintf("%+dms", row.DurationDeltaMS)
		fmt.Printf("%-18s %-13s %-13s %-10s %-10s %-12s %-10s\n", truncate(row.StepID, 18), before, after, input, output, usage, timing)
		if row.BeforeModel != row.AfterModel || row.BeforeRunner != row.AfterRunner {
			fmt.Printf("  %s\n", instMuted.Render(fmt.Sprintf("model/runner: %s/%s → %s/%s", row.BeforeModel, row.BeforeRunner, row.AfterModel, row.AfterRunner)))
		}
	}
	return nil
}

// cancelInstance stops one running instance through the daemon. It exists so a
// duplicate run can be recovered from inside the tool: `apiary restart` acts on a
// whole cell (and re-dispatches it), and `apiary instances` used to be read-only,
// which left killing the agent's process by hand as the only option (issue #422).
func cancelInstance(id string, asJSON bool) error {
	var stopped struct {
		Stopped string `json:"stopped"`
	}
	status, err := ipcDo(http.MethodPost, "/instances/stop/"+url.PathEscape(id), &stopped)
	if err != nil {
		switch status {
		case http.StatusNotFound:
			fmt.Println(instErr.Render("Instance not found: ") + id)
			os.Exit(2)
		case 0:
			return daemonDownHint()
		default:
			fmt.Println(instErr.Render("Error: ") + err.Error())
			os.Exit(1)
		}
	}
	if asJSON {
		line, _ := json.Marshal(map[string]string{"cancelled": id})
		fmt.Println(string(line))
		return nil
	}
	fmt.Println(instOK.Render("✓") + " Cancelled instance " + id +
		instMuted.Render(" (marked interrupted; continue it with `apiary resume "+id+"`)"))
	return nil
}

func listInstances(workflow, state string, limit int, asJSON, ticketsOnly bool) error {
	q := url.Values{}
	if workflow != "" {
		q.Set("workflow", workflow)
	}
	if state != "" {
		q.Set("state", state)
	}
	if ticketsOnly {
		q.Set("tickets_only", "1")
	}
	q.Set("limit", fmt.Sprintf("%d", limit))

	var resp daemon.InstancesResponse
	if err := ipcGetJSON("/instances?"+q.Encode(), &resp); err != nil {
		return daemonDownHint()
	}

	if asJSON {
		for _, in := range resp.Instances {
			line, _ := json.Marshal(in)
			fmt.Println(string(line))
		}
		return nil
	}

	if len(resp.Instances) == 0 {
		fmt.Println(instMuted.Render("No workflow instances yet."))
		return nil
	}

	fmt.Println(instHeader.Render(fmt.Sprintf("%-26s  %-22s  %-12s  %-18s  %-13s  %s",
		"ID", "WORKFLOW", "CELL", "STATE", "STARTED", "DURATION")))
	for _, in := range resp.Instances {
		fmt.Printf("%-26s  %-22s  %-12s  %s  %-13s  %s\n",
			in.ID,
			truncate(in.Workflow, 22),
			truncate(in.CellID, 12),
			stateCell(in.State, 18),
			in.Started,
			in.Duration,
		)
	}
	return nil
}

func showInstance(id string, asJSON bool) error {
	var detail daemon.InstanceDetail
	if err := ipcGetJSON("/instances/"+url.PathEscape(id), &detail); err != nil {
		if strings.Contains(err.Error(), "not found") {
			fmt.Println(instErr.Render("Instance not found: ") + id)
			os.Exit(2)
		}
		return daemonDownHint()
	}

	if asJSON {
		line, _ := json.MarshalIndent(detail, "", "  ")
		fmt.Println(string(line))
		return nil
	}

	cell := detail.CellID
	if detail.Title != "" {
		cell += " — " + detail.Title
	}
	fmt.Printf("%s  %s\n", instMuted.Render("Instance:"), detail.ID)
	fmt.Printf("%s  %s\n", instMuted.Render("Workflow:"), detail.Workflow)
	fmt.Printf("%s      %s\n", instMuted.Render("Cell:"), cell)
	fmt.Printf("%s     %s\n", instMuted.Render("State:"), stateGlyph(detail.State)+" "+detail.State)
	fmt.Printf("%s   %s\n\n", instMuted.Render("Started:"), detail.Started)

	if len(detail.Steps) == 0 {
		fmt.Println(instMuted.Render("No steps recorded."))
	} else {
		fmt.Println(instHeader.Render("Steps"))
		for _, s := range detail.Steps {
			state := s.State
			if s.Cached {
				state += " (cached)"
			}
			fmt.Printf("  %s  %-14s  %-16s  %-8s  %s\n",
				stepGlyph(s.State),
				truncate(s.StepID, 14),
				truncate(s.AgentID, 16),
				s.Duration,
				stateColor(s.State).Render(state),
			)
			if s.TotalTokens > 0 {
				usage := fmt.Sprintf("%s in / %s out / %s total", format.Tokens(s.InputTokens), format.Tokens(s.OutputTokens), format.Tokens(s.TotalTokens))
				if s.CacheCreationTokens > 0 || s.CacheReadTokens > 0 {
					usage += fmt.Sprintf("  ·  cache %s write / %s read", format.Tokens(s.CacheCreationTokens), format.Tokens(s.CacheReadTokens))
				}
				if s.CostUSD > 0 {
					usage += "  ·  " + format.USD(s.CostUSD)
				}
				fmt.Printf("       %s\n", instMuted.Render(usage))
			}
			if line := timingSummary(s.Timing); line != "" {
				fmt.Printf("       %s\n", instMuted.Render(line))
			}
		}
	}
	printCIPolls(detail.CIPolls)
	return nil
}

// printCIPolls prints the wait_for CI poll history — a header with the count and
// latest status, then the most recent poll rows (oldest of the window first).
// Nothing is printed when the instance never polled CI.
func printCIPolls(polls []daemon.CIPollView) {
	if len(polls) == 0 {
		return
	}
	const window = 10
	last := polls[len(polls)-1]
	fmt.Println()
	fmt.Println(instHeader.Render(fmt.Sprintf("CI Polls (%d)", len(polls))) +
		"  " + instMuted.Render("last: ") + pollColor(last.Status).Render(last.Status) +
		instMuted.Render(" · "+last.CheckedAt.Format("2006-01-02 15:04:05")))

	start := 0
	if len(polls) > window {
		start = len(polls) - window
		fmt.Println(instMuted.Render(fmt.Sprintf("  … %d earlier", start)))
	}
	for _, p := range polls[start:] {
		pad := 8 - len(p.Status)
		if pad < 0 {
			pad = 0
		}
		line := fmt.Sprintf("  %s  %s%s",
			instMuted.Render(p.CheckedAt.Format("2006-01-02 15:04:05")),
			pollColor(p.Status).Render(p.Status),
			strings.Repeat(" ", pad))
		if p.Detail != "" {
			line += "  " + instMuted.Render(truncate(p.Detail, 60))
		}
		fmt.Println(line)
	}
}

// pollColor maps a recorded CI poll status to a display style.
func pollColor(status string) lipgloss.Style {
	switch status {
	case "passed":
		return instOK
	case "failed", "timeout", "error":
		return instErr
	case "pending":
		return instWarn
	default:
		return instMuted
	}
}

// stateCell renders a state column, color-coded and padded to width.
func stateCell(state string, width int) string {
	pad := width - len(state)
	if pad < 0 {
		pad = 0
	}
	return stateColor(state).Render(state) + strings.Repeat(" ", pad)
}

func stateColor(st string) lipgloss.Style {
	switch apstate.Normalize(st) {
	case apstate.Done:
		return instOK
	case apstate.Failed:
		return instErr
	case apstate.Running, apstate.Blocked:
		return instWarn
	default:
		return instMuted
	}
}

func stateGlyph(st string) string {
	switch apstate.Normalize(st) {
	case apstate.Done:
		return instOK.Render("✓")
	case apstate.Failed:
		return instErr.Render("✗")
	case apstate.Running:
		return instWarn.Render("●")
	case apstate.Blocked:
		return instWarn.Render("⏸")
	default:
		return instMuted.Render("○")
	}
}

func stepGlyph(st string) string {
	switch apstate.Normalize(st) {
	case apstate.Done:
		return instOK.Render("✓")
	case apstate.Failed:
		return instErr.Render("✗")
	case apstate.Running:
		return instWarn.Render("●")
	case apstate.Blocked:
		return instWarn.Render("⏸")
	case apstate.Skipped:
		return instMuted.Render("⊘")
	default:
		return instMuted.Render("○")
	}
}

// ipcGetJSON performs a GET against the daemon's Unix socket and decodes the
// JSON body into v. A non-2xx response surfaces the body text as the error.
func ipcGetJSON(path string, v any) error {
	socketPath := daemon.SocketPath(config.DataDir(configFile))
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		},
	}
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}

	resp, err := client.Get("http://apiary" + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var b strings.Builder
		_, _ = b.WriteString(resp.Status)
		buf := make([]byte, 256)
		if n, _ := resp.Body.Read(buf); n > 0 {
			b.WriteString(": ")
			b.Write(buf[:n])
		}
		return fmt.Errorf("%s", strings.TrimSpace(b.String()))
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func daemonDownHint() error {
	fmt.Println(instMuted.Render("Daemon is not running.") +
		"  Start it with: " + instHeader.Render("apiary run"))
	return nil
}
