package cli

import (
	"context"
	"fmt"
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
			req, _ := http.NewRequest(http.MethodPost, url, nil)
			if tok := socketToken(); tok != "" {
				req.Header.Set("Authorization", "Bearer "+tok)
			}
			resp, err := client.Do(req)
			if err != nil {
				return fmt.Errorf("cannot reach daemon: %w", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("restart failed: HTTP %d", resp.StatusCode)
			}

			fmt.Printf("✓ Restarted cell %s (control labels cleared; re-enters the flow on the next poll)\n", cellID)
			return nil
		},
	}
}
