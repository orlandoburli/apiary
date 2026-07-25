package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/spf13/cobra"
)

func newEnvCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Manage named environments",
		Long: `Commands for listing, promoting, and rolling back named environments
defined in apiary.yaml.`,
	}
	cmd.AddCommand(
		newEnvListCmd(),
		newEnvPromoteCmd(),
		newEnvRollbackCmd(),
	)
	return cmd
}

func newEnvListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List named environments defined in apiary.yaml",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configFile)
			if err != nil {
				return err
			}
			if len(cfg.Environments) == 0 {
				fmt.Println("No environments defined. Add an `environments:` block to apiary.yaml.")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tDESCRIPTION\tSOURCES\tROLLOUT")
			for _, env := range cfg.Environments {
				resolved, err := cfg.ResolveEnvironment(env.Name)
				if err != nil {
					fmt.Fprintf(w, "%s\t%s\t(error: %v)\t\n", env.Name, env.Description, err)
					continue
				}
				sourceCount := len(resolved.Sources)
				rolloutDesc := "100%"
				if env.Rollout != nil {
					if env.Rollout.Percentage > 0 {
						rolloutDesc = fmt.Sprintf("%d%%", env.Rollout.Percentage)
					}
					if len(env.Rollout.Sources) > 0 {
						rolloutDesc += fmt.Sprintf(" (sources: %v)", env.Rollout.Sources)
					}
					if len(env.Rollout.Labels) > 0 {
						rolloutDesc += fmt.Sprintf(" (labels: %v)", env.Rollout.Labels)
					}
				}
				fmt.Fprintf(w, "%s\t%s\t%d sources\t%s\n",
					env.Name, env.Description, sourceCount, rolloutDesc)
			}
			return w.Flush()
		},
	}
}

func newEnvPromoteCmd() *cobra.Command {
	var dryRun bool
	var skipValidation bool

	cmd := &cobra.Command{
		Use:   "promote <from-env> <to-env>",
		Short: "Promote configuration from one environment to another",
		Long: `Gates a configuration promotion from one named environment to another.

Promotion checks:
  1. Validates both environments' resolved configurations.
  2. Performs a dry-run diff and reports any semantic differences.
  3. Records the promotion in the audit trail (config digest + git revision).

The promotion does not modify any files — it validates readiness and writes
an audit record. To actually change a production environment, update the
apiary.yaml overlays in Git and deploy the new config.

Use --dry-run to preview without writing an audit record.
Use --skip-validation to bypass validation checks (not recommended).`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			fromEnv, toEnv := args[0], args[1]

			cfg, err := config.Load(configFile)
			if err != nil {
				return err
			}

			fromResolved, err := resolveEnvOrBase(cfg, fromEnv)
			if err != nil {
				return fmt.Errorf("resolving source environment %q: %w", fromEnv, err)
			}
			toResolved, err := resolveEnvOrBase(cfg, toEnv)
			if err != nil {
				return fmt.Errorf("resolving target environment %q: %w", toEnv, err)
			}

			if !skipValidation {
				errs := fromResolved.Validate()
				if len(errs) > 0 {
					for _, e := range errs {
						fmt.Fprintf(cmd.ErrOrStderr(), "  ✗ [%s] %s\n", fromEnv, e)
					}
					return fmt.Errorf("source environment %q has %d validation error(s); fix before promoting", fromEnv, len(errs))
				}
				errs = toResolved.Validate()
				if len(errs) > 0 {
					for _, e := range errs {
						fmt.Fprintf(cmd.ErrOrStderr(), "  ✗ [%s] %s\n", toEnv, e)
					}
					return fmt.Errorf("target environment %q has %d validation error(s); fix before promoting", toEnv, len(errs))
				}
			}

			// Show semantic diff.
			sections := semanticDiff(fromEnv, toEnv, fromResolved, toResolved)
			if len(sections) == 0 {
				fmt.Printf("No semantic differences between %q and %q.\n", fromEnv, toEnv)
			} else {
				fmt.Printf("Semantic diff (%s → %s):\n", fromEnv, toEnv)
				for _, s := range sections {
					fmt.Println(s)
				}
			}

			fromDigest := fromResolved.Digest()
			toDigest := toResolved.Digest()
			gitRev := config.GitRevision(configFile)
			fmt.Printf("\n%s digest: %s\n", fromEnv, fromDigest)
			fmt.Printf("%s digest: %s\n", toEnv, toDigest)
			if gitRev != "" {
				fmt.Printf("git revision: %s\n", gitRev)
			}

			if dryRun {
				fmt.Println("\n[dry-run] Promotion checks passed. No audit record written.")
				return nil
			}

			// Write audit record to the database.
			if err := writePromotionRecord(cmd.Context(), fromEnv, toEnv, fromDigest, toDigest, gitRev); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "  ⚠ audit record could not be written: %v\n", err)
			} else {
				fmt.Printf("\nPromotion audit record written (git: %s).\n", gitRev)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "validate and diff without writing an audit record")
	cmd.Flags().BoolVar(&skipValidation, "skip-validation", false, "skip validation checks before promoting")
	return cmd
}

func newEnvRollbackCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rollback",
		Short: "Show the last known-good configuration revision from the audit trail",
		Long: `Queries the local database for the most recently recorded successful
workflow instance and reports its config digest and git revision.

To restore that revision:
  git checkout <git-revision>

The rollback command never modifies files; it only reads the audit trail
and tells you which revision to restore.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			dbPath := getDBPath()
			if _, err := os.Stat(dbPath); os.IsNotExist(err) {
				return fmt.Errorf("no database found at %s; run `apiary run` first", dbPath)
			}
			ctx := context.Background()
			dbClient, err := db.New(ctx, dbPath)
			if err != nil {
				return fmt.Errorf("opening database: %w", err)
			}
			defer dbClient.Close()

			rev, err := dbClient.LastSuccessfulRevision(ctx)
			if err != nil {
				return fmt.Errorf("querying last successful revision: %w", err)
			}
			if rev == nil {
				fmt.Println("No successful workflow instances with a recorded config digest found.")
				fmt.Println("Run `apiary run` with this version of apiary (v0.x+) to start recording revisions.")
				return nil
			}

			fmt.Printf("Last successful config revision:\n")
			fmt.Printf("  digest:   %s\n", rev.ConfigDigest)
			if rev.GitRevision != "" {
				fmt.Printf("  git:      %s\n", rev.GitRevision)
				fmt.Printf("  restore:  git checkout %s\n", rev.GitRevision)
			}
			fmt.Printf("  instance: %s\n", rev.InstanceID)
			fmt.Printf("  workflow: %s\n", rev.WorkflowID)
			fmt.Printf("  at:       %s\n", rev.CreatedAt.Format("2006-01-02 15:04:05"))
			return nil
		},
	}
}

// writePromotionRecord writes a promotion audit entry as a structured event
// to the database. It opens the project database but is best-effort: a missing
// or inaccessible database does not fail the promote command.
func writePromotionRecord(ctx context.Context, fromEnv, toEnv, fromDigest, toDigest, gitRev string) error {
	dbPath := getDBPath()
	if _, err := os.Stat(filepath.Dir(dbPath)); os.IsNotExist(err) {
		return fmt.Errorf("data directory %s does not exist; run `apiary run` first", filepath.Dir(dbPath))
	}
	dbClient, err := db.New(ctx, dbPath)
	if err != nil {
		return err
	}
	defer dbClient.Close()
	return dbClient.RecordExecutionEvent(ctx, &db.ExecutionEvent{
		Type: "env.promoted",
		Metadata: map[string]any{
			"from_env":    fromEnv,
			"to_env":      toEnv,
			"from_digest": fromDigest,
			"to_digest":   toDigest,
			"git_revision": gitRev,
		},
	})
}
