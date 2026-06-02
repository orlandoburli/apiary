package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/orlandoburli/apiary/internal/daemon"
)

var (
	statusBrand   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#F5A623"))
	statusMuted   = lipgloss.NewStyle().Foreground(lipgloss.Color("#626262"))
	statusSuccess = lipgloss.NewStyle().Foreground(lipgloss.Color("#04B575"))
	statusKey     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#DDDDDD"))
)

func newStatusCmd() *cobra.Command {
	var watch bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show daemon status and active runs",
		RunE: func(cmd *cobra.Command, args []string) error {
			if watch {
				return runWatch()
			}
			return printStatus()
		},
	}

	cmd.Flags().BoolVar(&watch, "watch", false, "refresh every 2 seconds")
	return cmd
}

func printStatus() error {
	resp, err := fetchStatus()
	if err != nil {
		fmt.Println(statusMuted.Render("Daemon is not running.") +
			"  Start it with: " + statusKey.Render("apiary run"))
		return nil
	}
	fmt.Print(renderStatus(resp))
	return nil
}

func runWatch() error {
	// render immediately, then refresh on ticker
	printStatus() //nolint:errcheck
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		fmt.Print("\033[H\033[2J") // clear screen
		printStatus()              //nolint:errcheck
	}
	return nil
}

func fetchStatus() (*daemon.StatusResponse, error) {
	socketPath := daemon.SocketPath()
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		},
	}
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}

	resp, err := client.Get("http://apiary/status")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var sr daemon.StatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, err
	}
	return &sr, nil
}

func renderStatus(r *daemon.StatusResponse) string {
	var b strings.Builder

	b.WriteString(
		statusBrand.Render("⬡ apiary") + "  " +
			statusMuted.Render("v"+r.Version) + "  " +
			statusMuted.Render("uptime: "+r.Uptime) + "  " +
			statusMuted.Render(r.ConfigFile) + "\n\n",
	)

	// sources
	b.WriteString(statusKey.Render("Sources") + "\n")
	if len(r.Sources) == 0 {
		b.WriteString(statusMuted.Render("  no sources configured") + "\n")
	} else {
		for _, s := range r.Sources {
			extra := ""
			if s.InFlight > 0 {
				extra = "  " + statusSuccess.Render(fmt.Sprintf("%d in-flight", s.InFlight))
			}
			b.WriteString(fmt.Sprintf("  %-18s %-8s  last: %-14s  found: %d%s\n",
				s.ID, s.Type, s.LastPoll, s.LastCount, extra))
		}
	}
	b.WriteString("\n")

	// active runs
	label := fmt.Sprintf("%d / %d", r.Concurrency.Active, r.Concurrency.Max)
	b.WriteString(statusKey.Render("Active Runs") + "  " + statusMuted.Render(label) + "\n")
	if len(r.ActiveRuns) == 0 {
		b.WriteString(statusMuted.Render("  no active runs") + "\n")
	} else {
		for _, run := range r.ActiveRuns {
			b.WriteString(fmt.Sprintf("  %-10s  %-38s  %-16s  %-22s  %s\n",
				run.ID,
				truncate(run.Title, 38),
				run.WorkerID,
				truncate(run.Model, 22),
				statusSuccess.Render(run.Elapsed),
			))
		}
	}
	b.WriteString("\n")

	return b.String()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
