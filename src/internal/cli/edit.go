package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/editor"
)

func newEditCmd() *cobra.Command {
	var workflowID string

	cmd := &cobra.Command{
		Use:   "edit [file]",
		Short: "Open the visual workflow editor",
		Long: `Open a bidirectional TUI editor for an apiary.yaml workflow.

The editor lets you navigate, create, and connect steps visually while keeping
YAML as the canonical representation. Changes are validated inline and a diff
is shown before every save.

Unsupported YAML constructs (anchors, aliases) are presented read-only and are
never silently removed.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Resolve config file path (flag or positional arg)
			path := configFile
			if len(args) > 0 {
				path = args[0]
			}

			// Read raw YAML before env expansion (for round-trip preservation).
			raw, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("reading %s: %w", path, err)
			}

			// Load via the full config pipeline (validation is not run here; the
			// editor runs it on-demand so the user can start editing an invalid file).
			cfg, err := config.Load(path)
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			if len(cfg.Workflows) == 0 {
				return fmt.Errorf("no workflows defined in %s — add a workflows: section first", path)
			}

			// Find the workflow to edit.
			idx, err := resolveWorkflowIndex(cfg, workflowID)
			if err != nil {
				return err
			}

			return editor.Run(cfg, path, idx, string(raw))
		},
	}

	cmd.Flags().StringVarP(&workflowID, "workflow", "w", "", "workflow ID to edit (default: first workflow)")
	return cmd
}

// resolveWorkflowIndex returns the slice index of the workflow to edit.
// When id is empty, the first workflow is returned; a picker is shown when
// multiple workflows exist and no id is specified.
func resolveWorkflowIndex(cfg *config.Config, id string) (int, error) {
	if id != "" {
		for i, wf := range cfg.Workflows {
			if wf.ID == id {
				return i, nil
			}
		}
		return 0, fmt.Errorf("workflow %q not found; available: %s",
			id, workflowIDList(cfg))
	}

	// Single workflow: open it directly.
	if len(cfg.Workflows) == 1 {
		return 0, nil
	}

	// Multiple workflows without an explicit choice: print the list and return
	// an error guiding the user to pass --workflow.
	fmt.Fprintf(os.Stderr, "Multiple workflows found. Use --workflow <id> to select one:\n")
	for _, wf := range cfg.Workflows {
		desc := ""
		if wf.Description != "" {
			desc = "  — " + wf.Description
		}
		fmt.Fprintf(os.Stderr, "  %s%s\n", wf.ID, desc)
	}
	return 0, fmt.Errorf("specify --workflow <id>")
}

func workflowIDList(cfg *config.Config) string {
	ids := make([]string, len(cfg.Workflows))
	for i, wf := range cfg.Workflows {
		ids[i] = wf.ID
	}
	result := ""
	for i, id := range ids {
		if i > 0 {
			result += ", "
		}
		result += id
	}
	return result
}
