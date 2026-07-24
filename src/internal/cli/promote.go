package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/spf13/cobra"
)

func newPromoteCmd() *cobra.Command {
	var skipValidate bool
	var note string

	cmd := &cobra.Command{
		Use:   "promote <from-env> <to-env>",
		Short: "Promote a configuration from one environment to another",
		Long: `promote validates the source environment config, optionally presents a semantic
diff, records an auditable promotion entry in the config_revisions table, and
reports the resulting digest.

Use "base" as the source to promote the unmodified config.

The promotion does NOT modify apiary.yaml — it records that the named config
digest was promoted to the target environment at this point in time.

Examples:
  apiary promote base staging
  apiary promote staging production
  apiary promote staging production --note "release v2.4.0"`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			fromEnv, toEnv := args[0], args[1]

			cfg, err := config.Load(configFile)
			if err != nil {
				return err
			}

			from, err := resolveEnvConfig(cfg, fromEnv)
			if err != nil {
				return fmt.Errorf("resolving source env %q: %w", fromEnv, err)
			}
			to, err := resolveEnvConfig(cfg, toEnv)
			if err != nil {
				return fmt.Errorf("resolving target env %q: %w", toEnv, err)
			}

			// Validate the source config unless skipped.
			if !skipValidate {
				label := fromEnv
				if fromEnv == "base" {
					label = "base config"
				} else {
					label = "environments." + fromEnv
				}
				if err := validateConfig(cmd, from, label); err != nil {
					return fmt.Errorf("source config invalid: %w", err)
				}
			}

			// Show semantic diff.
			diffLines := semanticDiff(fromEnv, toEnv, from, to)
			if len(diffLines) > 0 {
				fmt.Printf("Diff %s → %s:\n", fromEnv, toEnv)
				for _, l := range diffLines {
					fmt.Println(l)
				}
				fmt.Println()
			} else {
				fmt.Printf("No differences between %q and %q\n\n", fromEnv, toEnv)
			}

			fromDigest := config.Digest(from)
			gitRev := config.CurrentGitRevision(configFile)

			fmt.Printf("Promoting %q → %q\n", fromEnv, toEnv)
			fmt.Printf("  source digest:  %s\n", fromDigest)
			if gitRev != "" {
				fmt.Printf("  git revision:   %s\n", gitRev)
			}
			if note != "" {
				fmt.Printf("  note:           %s\n", note)
			}

			// Record the promotion in the DB if available.
			ctx := context.Background()
			dbPath := filepath.Join(config.DataDir(configFile), "apiary.db")
			dbClient, dbErr := db.New(ctx, dbPath)
			if dbErr != nil {
				fmt.Printf("\nWarning: could not open database to record audit entry: %v\n", dbErr)
				fmt.Println("Promotion noted locally only.")
				return nil
			}
			defer dbClient.Close()

			id := fmt.Sprintf("rev_%d", time.Now().UnixNano())
			rev := &db.ConfigRevision{
				ID:              id,
				Environment:     toEnv,
				ConfigDigest:    fromDigest,
				GitRevision:     gitRev,
				Event:           "promote",
				FromEnvironment: fromEnv,
				Note:            note,
			}
			if err := dbClient.RecordConfigRevision(ctx, rev); err != nil {
				return fmt.Errorf("recording promotion: %w", err)
			}

			fmt.Printf("\n✓ Promotion recorded (id: %s)\n", id)
			return nil
		},
	}

	cmd.Flags().BoolVar(&skipValidate, "skip-validate", false, "skip source environment validation before promoting")
	cmd.Flags().StringVar(&note, "note", "", "optional note to attach to the audit record")
	return cmd
}
