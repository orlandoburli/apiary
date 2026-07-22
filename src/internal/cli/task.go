package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/orlandoburli/apiary/internal/daemon"
)

// newTaskCmd renders the full workflow history of a single InternalTask: every
// workflow instance it ran (e.g. investigator → implementation), each as a labeled
// section with its steps and the log lines scoped to that instance's time window.
func newTaskCmd() *cobra.Command {
	var (
		source string
		item   string
		asJSON bool
	)

	cmd := &cobra.Command{
		Use:   "task [internal-task-id]",
		Short: "Show a task's full workflow history (all instances, steps, scoped logs)",
		Long: "Show a task's full workflow history. Pass the InternalTask id, or locate it\n" +
			"from a source item with --source and --item (e.g. --source github --item 1948).",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{}
			switch {
			case len(args) == 1:
				q.Set("task", args[0])
			case source != "" && item != "":
				q.Set("source", source)
				q.Set("item", item)
			default:
				return fmt.Errorf("provide a task id, or both --source and --item")
			}

			var resp daemon.TaskHistoryResponse
			if err := ipcGetJSON("/tasks/history?"+q.Encode(), &resp); err != nil {
				if strings.Contains(err.Error(), "not found") {
					fmt.Println(instErr.Render("No task history found."))
					os.Exit(2)
				}
				return daemonDownHint()
			}

			if asJSON {
				line, _ := json.MarshalIndent(resp, "", "  ")
				fmt.Println(string(line))
				return nil
			}
			renderTaskHistory(resp)
			return nil
		},
	}

	cmd.Flags().StringVar(&source, "source", "", "resolve the task by source id (with --item), e.g. github")
	cmd.Flags().StringVar(&item, "item", "", "resolve the task by source item id/number (with --source), e.g. 1948")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output as JSON")
	return cmd
}

// renderTaskHistory prints the per-instance history top-to-bottom (oldest first):
// for each workflow instance, a section header, its steps, then its scoped logs.
func renderTaskHistory(resp daemon.TaskHistoryResponse) {
	head := resp.TaskID
	if resp.Title != "" {
		head += " — " + resp.Title
	}
	fmt.Printf("%s  %s\n", instMuted.Render("Task:"), head)
	if len(resp.Events) > 0 {
		fmt.Println()
		fmt.Println(instHeader.Render("Timeline"))
		for _, event := range resp.Events {
			fmt.Printf("  %s %-24s %s\n", instMuted.Render(event.Timestamp.Local().Format("15:04:05")), event.Type, event.StepID)
		}
	}

	if len(resp.Segments) == 0 {
		fmt.Println(instMuted.Render("No workflow instances yet."))
		return
	}

	for _, seg := range resp.Segments {
		in := seg.Instance
		fmt.Println()
		// ── <glyph> <workflow> · <state> · <started> (<duration>) ──
		header := fmt.Sprintf("%s %s · %s · %s (%s)",
			stateGlyph(in.State), instHeader.Render(in.Workflow), in.State, in.Started, in.Duration)
		fmt.Println(instMuted.Render("── ") + header + instMuted.Render(" ──"))

		for _, s := range seg.Steps {
			state := s.State
			if s.Cached {
				state += " (cached)"
			}
			fmt.Printf("  %s %-16s %-14s %-8s %s\n",
				stepGlyph(s.State), truncate(s.StepID, 16), truncate(s.AgentID, 14),
				s.Duration, stateColor(s.State).Render(state))
			if s.Summary != "" {
				fmt.Println(instMuted.Render("      " + truncate(s.Summary, 100)))
			}
		}

		printCIPolls(seg.CIPolls)

		for _, l := range seg.Logs {
			fmt.Printf("  %s %s %s\n",
				instMuted.Render(l.Timestamp.Format("15:04:05")), logLevelTag(l.Level), l.Message)
		}
	}
}

// logLevelTag renders a fixed-width, color-coded log level.
func logLevelTag(level string) string {
	switch strings.ToUpper(level) {
	case "ERROR":
		return instErr.Render("ERROR")
	case "WARN":
		return instWarn.Render("WARN ")
	default:
		return instMuted.Render(fmt.Sprintf("%-5s", strings.ToUpper(level)))
	}
}
