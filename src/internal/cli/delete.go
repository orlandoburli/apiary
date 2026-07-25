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

// newDeleteCmd deletes a task from the database, allowing it to be picked up
// fresh the next time the dispatcher runs.
func newDeleteCmd() *cobra.Command {
	var (
		source string
		item   string
		yes    bool
	)

	cmd := &cobra.Command{
		Use:   "delete [task-id]",
		Short: "Delete a task and all its workflow instances from the database",
		Long: "Permanently delete a task and all its workflow instances, steps, and logs.\n" +
			"This allows the task to be picked up fresh on the next dispatch cycle.\n\n" +
			"Pass the task ID, or locate it with --source and --item (e.g., --source github --item 1953).",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var taskID string
			switch {
			case len(args) == 1:
				taskID = args[0]
			case source != "" && item != "":
				taskID = fmt.Sprintf("%s:%s", source, item)
			default:
				return fmt.Errorf("provide a task id, or both --source and --item")
			}

			// Warn the user before deleting
			if !yes {
				fmt.Println(instWarn.Render("This will permanently delete the task and all its history:"))
				fmt.Println("  " + taskID)
				if !confirm(instWarn.Render("Proceed with deletion?") + " [y/N] ") {
					fmt.Println(instMuted.Render("Aborted."))
					return nil
				}
			}

			dataDir := config.DataDir(configFile)
			socketPath := daemon.SocketPath(dataDir)
			transport := &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
				},
			}
			client := &http.Client{Transport: transport, Timeout: 5 * time.Second}

			req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://apiary/tasks/delete/%s", taskID), nil)
			if err != nil {
				return fmt.Errorf("building request: %w", err)
			}
			if token, err := daemon.ReadSocketToken(dataDir); err == nil {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			resp, err := client.Do(req)
			if err != nil {
				return fmt.Errorf("cannot reach daemon: %w", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				detail := strings.TrimSpace(string(body))
				if resp.StatusCode == http.StatusNotFound {
					return fmt.Errorf("no task matched %q — pass the task id, the source item id, or --source/--item", taskID)
				}
				if detail != "" {
					return fmt.Errorf("delete failed (HTTP %d): %s", resp.StatusCode, detail)
				}
				return fmt.Errorf("delete failed: HTTP %d", resp.StatusCode)
			}

			fmt.Println(instOK.Render("✓ Task deleted successfully."))
			return nil
		},
	}

	cmd.Flags().StringVar(&source, "source", "", "resolve the task by source id (with --item), e.g. github")
	cmd.Flags().StringVar(&item, "item", "", "resolve the task by source item id/number (with --source), e.g. 1953")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}
