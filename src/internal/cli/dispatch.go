package cli

import (
	"bytes"
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

func newDispatchCmd() *cobra.Command {
	var (
		item   string
		title  string
		inputs []string
	)

	cmd := &cobra.Command{
		Use:   "dispatch <workflow-id>",
		Short: "Start a workflow manually",
		Long: `Start one named workflow right now, whether or not anything would have
triggered it.

A manual run skips every gate the poll loop applies:

  • the trigger's match block — states, labels, type, title_regex, source
  • exclusive-trigger suppression
  • the live-instance guard, so a workflow already running on the task starts a
    SECOND concurrent instance
  • ` + "`once: true`" + `, and the consecutive-failure cap (settings.max_attempts)

With --item the run binds an existing source item and behaves exactly like an
automatic dispatch of that workflow: the same live labels and state, and side
effects (comments, state locks, sub-issues) write back to the source. <item> is
the source item id or its human reference (CDT-123, #1953), the same vocabulary
` + "`apiary restart`" + ` accepts.

Without --item the workflow runs standalone on a fresh internal task with no
source binding. Nothing writes back to a source: comment and state-lock steps
are no-ops and sub-issues cannot be materialized. Use --input to pass values the
steps read as ${{ input.<key> }}.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			workflowID := strings.TrimSpace(args[0])
			if workflowID == "" {
				return fmt.Errorf("a workflow id is required")
			}
			input, err := parseInputPairs(inputs)
			if err != nil {
				return err
			}

			body, err := json.Marshal(map[string]any{"input": input, "title": title})
			if err != nil {
				return fmt.Errorf("encoding request: %w", err)
			}

			socketPath := daemon.SocketPath(config.DataDir(configFile))
			transport := &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
				},
			}
			client := &http.Client{Transport: transport, Timeout: 30 * time.Second}

			url := "http://apiary/workflows/" + neturl.PathEscape(workflowID) + "/run"
			if item != "" {
				url += "?item=" + neturl.QueryEscape(item)
			}
			resp, err := client.Post(url, "application/json", bytes.NewReader(body))
			if err != nil {
				return fmt.Errorf("cannot reach daemon: %w", err)
			}
			defer resp.Body.Close()

			payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			if resp.StatusCode != http.StatusAccepted {
				if msg := strings.TrimSpace(string(payload)); msg != "" {
					return fmt.Errorf("dispatch failed: %s", msg)
				}
				return fmt.Errorf("dispatch failed: HTTP %d", resp.StatusCode)
			}

			var res daemon.ManualRunResult
			if err := json.Unmarshal(payload, &res); err != nil {
				fmt.Printf("✓ Started workflow %s\n", workflowID)
				return nil
			}

			fmt.Printf("✓ Started workflow %s on %s\n", res.WorkflowID, res.Label())
			if res.Concurrent {
				fmt.Printf("  ! this workflow was already running on the task — a second instance is now live\n")
			}
			if res.Standalone {
				fmt.Printf("  → no source item bound: comments, state locks and sub-issues are no-ops for this run\n")
			}
			for _, b := range res.Bypassed {
				fmt.Printf("  ! bypassed guard: %s\n", b)
			}
			fmt.Printf("  → follow it with: apiary instances\n")
			return nil
		},
	}

	cmd.Flags().StringVar(&item, "item", "", "source item to run against (item id or reference like CDT-123, #1953); omit to run standalone")
	cmd.Flags().StringVar(&title, "title", "", "title for the task created by a standalone run")
	cmd.Flags().StringArrayVar(&inputs, "input", nil, "key=value passed to a standalone run's task input, readable as ${{ input.<key> }} (repeatable)")
	return cmd
}

// parseInputPairs turns repeated --input key=value flags into the task input map.
// Values stay strings: the flag has no type information, and a workflow reading
// ${{ input.x }} interpolates it as text either way.
func parseInputPairs(pairs []string) (map[string]any, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	out := make(map[string]any, len(pairs))
	for _, p := range pairs {
		key, value, ok := strings.Cut(p, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid --input %q: expected key=value", p)
		}
		out[key] = value
	}
	return out, nil
}
