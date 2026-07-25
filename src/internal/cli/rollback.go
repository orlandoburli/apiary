package cli

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/orlandoburli/apiary/internal/db"
	"github.com/spf13/cobra"
)

func newRollbackCmd() *cobra.Command {
	var (
		envName string
		digest  string
		list    bool
		limit   int
	)

	cmd := &cobra.Command{
		Use:   "rollback",
		Short: "Restore a previously promoted environment configuration",
		Long: `Restore a previously recorded environment configuration revision.

Use --list to show recent revisions for an environment without rolling back.
Use --env and --digest to roll back to a specific revision. When --digest is
omitted, the second-most-recent revision is restored (i.e. one step back).

Rollback writes the stored config YAML to stdout — redirect it to apiary.yaml
or a staging copy, review the diff, then reload the daemon.

Examples:
  apiary rollback --env production --list
  apiary rollback --env production --digest a1b2c3d4e5f60708`,
		RunE: func(cmd *cobra.Command, args []string) error {
			dbPath := getDBPath()
			ctx := context.Background()
			dbClient, err := db.New(ctx, dbPath)
			if err != nil {
				return fmt.Errorf("opening database: %w", err)
			}
			defer dbClient.Close()

			if list {
				revs, err := dbClient.ListEnvironmentRevisions(ctx, envName, limit)
				if err != nil {
					return err
				}
				if len(revs) == 0 {
					if envName != "" {
						fmt.Printf("no revisions found for environment %q\n", envName)
					} else {
						fmt.Println("no revisions recorded yet")
					}
					return nil
				}
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "ID\tENV\tDIGEST\tGIT REV\tPROMOTED BY\tCREATED AT")
				for _, r := range revs {
					fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n",
						r.ID, r.EnvName, r.ConfigDigest, r.GitRevision,
						r.PromotedBy, r.CreatedAt.Format("2006-01-02 15:04:05"))
				}
				return w.Flush()
			}

			if envName == "" {
				return fmt.Errorf("--env is required for rollback (use --list to see available revisions)")
			}

			var target *db.EnvironmentRevision

			if digest != "" {
				target, err = dbClient.GetEnvironmentRevisionByDigest(ctx, envName, digest)
				if err != nil {
					return err
				}
				if target == nil {
					return fmt.Errorf("no revision with digest %q found for environment %q", digest, envName)
				}
			} else {
				// No digest — restore the previous revision (second-most-recent).
				revs, err := dbClient.ListEnvironmentRevisions(ctx, envName, 2)
				if err != nil {
					return err
				}
				if len(revs) < 2 {
					return fmt.Errorf("need at least 2 revisions to roll back; use --digest to target a specific one")
				}
				target = &revs[1]
			}

			fmt.Fprintf(os.Stderr, "Rolling back environment %q to revision %d (digest %s, git %s)\n",
				envName, target.ID, target.ConfigDigest, target.GitRevision)
			fmt.Fprintf(os.Stderr, "Promoted by %q at %s\n\n",
				target.PromotedBy, target.CreatedAt.Format("2006-01-02 15:04:05"))

			// Print the stored YAML to stdout so the operator can review and redirect.
			fmt.Print(target.ConfigYAML)
			return nil
		},
	}

	cmd.Flags().StringVar(&envName, "env", "", "target environment name")
	cmd.Flags().StringVar(&digest, "digest", "", "specific config digest to restore (omit for one-step rollback)")
	cmd.Flags().BoolVar(&list, "list", false, "list recent revisions instead of rolling back")
	cmd.Flags().IntVar(&limit, "limit", 20, "number of revisions to show with --list")
	return cmd
}
