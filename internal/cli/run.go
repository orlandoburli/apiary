package cli

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/orlandoburli/apiary/internal/tui"
)

func newRunCmd() *cobra.Command {
	var (
		dryRun bool
		once   bool
		source string
		worker string
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Start the Apiary daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			if dryRun {
				fmt.Println("dry-run mode: no runners will be invoked")
			}

			m := tui.New()
			p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
			if _, err := p.Run(); err != nil {
				return fmt.Errorf("tui: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "fetch and match tasks but do not invoke runners")
	cmd.Flags().BoolVar(&once, "once", false, "poll once, process pending tasks, then exit")
	cmd.Flags().StringVar(&source, "source", "", "restrict to a single source id")
	cmd.Flags().StringVar(&worker, "worker", "", "restrict to a single worker id")

	return cmd
}
