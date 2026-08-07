package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/daemon"
)

func newRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart <cell-id>",
		Short: "Force-restart a stale task",
		Long: `Kill the running dispatch for the given cell, reset its state, and
re-queue it so the next poll picks it up again.

<cell-id> is the source item id — the GitHub issue number, the Jira issue id, the
key shown in the dashboard's task rows — NOT the internal task id. An id the
daemon cannot resolve to a known cell fails with "unknown cell" and nothing is
touched.

Also strips the cell's control labels — the lock (e.g. "in-progress") and the
stage marker (e.g. "agent:engineer") — so the task re-enters the flow from the
start instead of being shadowed by a stale label. The labels removed are derived
from the routes' exclude_label_prefix / exclude_labels.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cellID := strings.TrimSpace(args[0])
			if cellID == "" {
				return fmt.Errorf("cell id is required")
			}

			socketPath := daemon.SocketPath(config.DataDir(configFile))
			transport := &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
				},
			}
			client := &http.Client{Transport: transport, Timeout: 5 * time.Second}

			url := fmt.Sprintf("http://apiary/restart/%s", cellID)
			resp, err := client.Post(url, "application/json", nil)
			if err != nil {
				return fmt.Errorf("cannot reach daemon: %w", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				// Surface the daemon's message (e.g. "unknown cell 019fd93…") —
				// a bare status code hid which id actually got restarted (#377).
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
				if msg := strings.TrimSpace(string(body)); msg != "" {
					return fmt.Errorf("restart failed: %s", msg)
				}
				return fmt.Errorf("restart failed: HTTP %d", resp.StatusCode)
			}

			fmt.Printf("✓ Restarted cell %s (control labels cleared; re-enters the flow on the next poll)\n", cellID)
			return nil
		},
	}
}
