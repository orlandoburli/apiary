package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/daemon"
	"github.com/orlandoburli/apiary/internal/dashboard"
	"github.com/orlandoburli/apiary/internal/db"
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
	dbPath := getDBPath()
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("database not found at %s\nRun 'apiary run' in another terminal first", dbPath)
	}

	dbConn, err := db.New(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer func() { _ = dbConn.Close() }()

	cfg, err := config.Load(configFile)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	socketPath := daemon.SocketPath(config.DataDir(configFile))
	app := dashboard.New(dbConn, socketPath, cfg)
	if err := app.Run(); err != nil {
		return fmt.Errorf("dashboard error: %w", err)
	}

	return nil
}
