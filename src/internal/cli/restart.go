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
re-queue it so the next poll picks it up again.`,
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
				return fmt.Errorf("restart failed: HTTP %d", resp.StatusCode)
			}

			fmt.Printf("✓ Restarted cell %s\n", cellID)
			return nil
		},
	}
}
