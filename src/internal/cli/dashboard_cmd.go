package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/orlandoburli/apiary/internal/dashboard"
)

func newDashboardCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dashboard",
		Short: "Open the dashboard TUI (requires running dispatcher)",
		Long: `Open the Apiary dashboard to monitor task execution, agent status, and logs.

The dashboard reads from the SQLite database populated by a running dispatcher.
Run 'apiary run' in another terminal first.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDashboard(cmd.Context())
		},
	}
}

func runDashboard(ctx context.Context) error {
	// Create and run dashboard
	app := dashboard.New()
	if err := app.Run(); err != nil {
		return fmt.Errorf("dashboard error: %w", err)
	}

	return nil
}
