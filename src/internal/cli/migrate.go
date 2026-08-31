package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/orlandoburli/apiary/internal/db"
)

// newMigrateCmd exposes the data migrations as an explicit operator action.
//
// The daemon runs them itself at startup, so this command is not normally
// required. It exists for the upgrade a cautious operator wants to perform
// deliberately — with the daemon stopped and a copy of the database taken —
// rather than as a side effect of the next `apiary run`.
func newMigrateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Apply pending database data migrations",
		Long: "Apply pending database data migrations.\n\n" +
			"Schema creation (tables, indices, new columns) happens automatically\n" +
			"whenever any command opens the database. This command runs the one-shot\n" +
			"data repairs and rewrites, which the daemon also runs at startup.\n\n" +
			"Stop the daemon before running it. The migrations rewrite rows and one\n" +
			"of them recreates a table, so a concurrent writer can lose a row that\n" +
			"lands mid-rebuild. Every step is idempotent: running this on an\n" +
			"already-migrated database does nothing.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			dbPath := getDBPath()
			dbClient, err := db.New(ctx, dbPath)
			if err != nil {
				return fmt.Errorf("opening database: %w", err)
			}
			defer dbClient.Close()

			if err := dbClient.MigrateData(ctx); err != nil {
				return fmt.Errorf("migrating database: %w", err)
			}

			fmt.Printf("Database migrated: %s\n", dbPath)
			return nil
		},
	}
}
