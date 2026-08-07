package cli

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

	"github.com/spf13/cobra"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/daemon"
)

func newRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart <cell-id>",
		Short: "Force-restart a stale task",
		Long: `Kill the running dispatch for the given cell, reset its state, and
dispatch it again immediately.

<cell-id> is the source item id — the GitHub issue number, the Jira issue id, the
key shown in the dashboard's task rows — NOT the internal task id. An id the
daemon cannot resolve to a known cell fails with "unknown cell" and nothing is
touched.

Also strips the cell's control labels — the lock (e.g. "in-progress") and the
stage marker (e.g. "agent:engineer") — so the task re-enters the flow from the
start instead of being shadowed by a stale label. The labels removed are derived
from the routes' exclude_label_prefix / exclude_labels.

Restart overrides the ` + "`once`" + ` and failure-cap guards, since a task wedged behind
either is exactly what restart exists to unwedge; overrides are reported. It does
not override the in-flight guard, so a live workflow is never run twice.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := strings.TrimSpace(args[0])
			if ref == "" {
				return fmt.Errorf("a cell id or item reference is required")
			}

			socketPath := daemon.SocketPath(config.DataDir(configFile))
			transport := &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
				},
			}
			client := &http.Client{Transport: transport, Timeout: 5 * time.Second}

			// PathEscape, not raw: a GitHub reference is "#1953" and a bare '#'
			// would be parsed as a URL fragment, so the daemon would see an empty id.
			url := "http://apiary/restart/" + neturl.PathEscape(ref)
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

			var res daemon.RestartResult
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			if err := json.Unmarshal(body, &res); err != nil {
				// An older daemon answers 200 with an empty body. The restart did
				// happen; only the detail is missing.
				fmt.Printf("✓ Restarted %s (control labels cleared)\n", ref)
				return nil
			}

			fmt.Printf("✓ Restarted %s (control labels cleared)\n", res.Label())
			for _, o := range res.Overridden {
				fmt.Printf("  ! overrode guard: %s\n", o)
			}
			if res.Dispatched == 0 {
				fmt.Printf("  → no workflow matches the item right now; nothing dispatched\n")
				return nil
			}
			fmt.Printf("  → dispatched %d workflow(s): %s\n", res.Dispatched, strings.Join(res.Workflows, ", "))
			return nil
		},
	}
}
