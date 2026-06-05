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
		workflow string
		state    string
		limit    int
		asJSON   bool
	)

	cmd := &cobra.Command{
		Use:   "instances [instance-id]",
		Short: "List workflow instances or show one in detail",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				return showInstance(args[0], asJSON)
			}
			return listInstances(workflow, state, limit, asJSON)
		},
	}

	cmd.Flags().StringVar(&workflow, "workflow", "", "filter by workflow id")
	cmd.Flags().StringVar(&state, "state", "", "filter by state (pending, running, approval_waiting, interrupted, done, failed)")
	cmd.Flags().IntVar(&limit, "limit", 20, "max rows")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output as JSON")
	return cmd
}

func listInstances(workflow, state string, limit int, asJSON bool) error {
	q := url.Values{}
	if workflow != "" {
		q.Set("workflow", workflow)
	}
	if state != "" {
		q.Set("state", state)
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
		return nil
	}
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
	}
	return nil
}

// stateCell renders a state column, color-coded and padded to width.
func stateCell(state string, width int) string {
	pad := width - len(state)
	if pad < 0 {
		pad = 0
	}
	return stateColor(state).Render(state) + strings.Repeat(" ", pad)
}

func stateColor(state string) lipgloss.Style {
	switch state {
	case "passed", "done":
		return instOK
	case "failed":
		return instErr
	case "running", "approval_waiting":
		return instWarn
	default:
		return instMuted
	}
}

func stateGlyph(state string) string {
	switch state {
	case "done":
		return instOK.Render("✓")
	case "failed":
		return instErr.Render("✗")
	case "running":
		return instWarn.Render("●")
	case "approval_waiting":
		return instWarn.Render("⏸")
	default:
		return instMuted.Render("○")
	}
}

func stepGlyph(state string) string {
	switch state {
	case "passed":
		return instOK.Render("✓")
	case "failed":
		return instErr.Render("✗")
	case "running":
		return instWarn.Render("●")
	case "skipped", "skipped_cached":
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
