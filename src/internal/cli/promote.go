package cli

import (
	"context"
	"fmt"
	"os/user"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/spf13/cobra"
)

func newPromoteCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "promote <from> <to>",
		Short: "Promote a configuration from one environment to another",
		Long: `Promote a configuration from one named environment to another.

Before promotion, the resolved configuration for the target environment is
validated. When --dry-run is set, all checks are performed but no revision
record is written to the database.

The promotion is recorded in the environment_revisions table with the config
digest, git revision, and the OS user who ran the command. Use "apiary rollback"
to restore a previous revision.

Examples:
  apiary promote development staging
  apiary promote staging production --dry-run`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			fromName, toName := args[0], args[1]

			cfg, err := config.Load(configFile)
			if err != nil {
				return err
			}

			if _, ok := cfg.Environments[fromName]; !ok {
				return fmt.Errorf("source environment %q not found in config", fromName)
			}
			if _, ok := cfg.Environments[toName]; !ok {
				return fmt.Errorf("target environment %q not found in config", toName)
			}

			fromCfg, err := cfg.ResolveForEnvironment(fromName)
			if err != nil {
				return fmt.Errorf("resolving source environment %q: %w", fromName, err)
			}
			toCfg, err := cfg.ResolveForEnvironment(toName)
			if err != nil {
				return fmt.Errorf("resolving target environment %q: %w", toName, err)
			}

			// Validate the target resolved config.
			if errs := toCfg.Validate(); len(errs) > 0 {
				fmt.Printf("✗ resolved config for %q failed validation:\n", toName)
				for _, e := range errs {
					fmt.Printf("  ✗ %s\n", e)
				}
				return fmt.Errorf("target environment %q did not pass validation", toName)
			}

			fromDigest, _ := config.Digest(fromCfg)
			toDigest, _ := config.Digest(toCfg)
			gitRev := config.GitRevision(configFile)

			// Show semantic diff so the operator can confirm.
			entries := config.SemanticDiff(fromCfg, toCfg)
			fmt.Printf("Promoting %s → %s\n", fromName, toName)
			fmt.Printf("  from digest: %s\n", fromDigest)
			fmt.Printf("  to   digest: %s\n", toDigest)
			if gitRev != "" {
				fmt.Printf("  git revision: %s\n", gitRev)
			}
			if len(entries) == 0 {
				fmt.Println("  (no semantic differences between environments)")
			} else {
				fmt.Printf("\nSemantic diff (%d change(s)):\n", len(entries))
				for _, e := range entries {
					fmt.Println(" ", e.String())
				}
			}

			if dryRun {
				fmt.Println("\n✓ dry-run: validation passed, no revision recorded")
				return nil
			}

			// Persist the revision record.
			promotedBy := "unknown"
			if u, err := user.Current(); err == nil {
				promotedBy = u.Username
			}

			yamlBytes, err := config.MarshalYAML(toCfg)
			if err != nil {
				return fmt.Errorf("marshaling resolved config: %w", err)
			}

			dbPath := getDBPath()
			ctx := context.Background()
			dbClient, err := db.New(ctx, dbPath)
			if err != nil {
				return fmt.Errorf("opening database: %w", err)
			}
			defer dbClient.Close()

			rev := &db.EnvironmentRevision{
				EnvName:      toName,
				ConfigDigest: toDigest,
				GitRevision:  gitRev,
				ConfigYAML:   string(yamlBytes),
				PromotedBy:   promotedBy,
			}
			id, err := dbClient.SaveEnvironmentRevision(ctx, rev)
			if err != nil {
				return fmt.Errorf("saving revision: %w", err)
			}

			fmt.Printf("\n✓ promoted to %q (revision id %d, digest %s)\n", toName, id, toDigest)
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "validate and diff without writing a revision record")
	return cmd
}
