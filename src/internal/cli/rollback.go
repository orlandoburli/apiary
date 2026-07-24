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

func newRollbackCmd() *cobra.Command {
	var env string
	var toDigest string
	var list bool
	var note string

	cmd := &cobra.Command{
		Use:   "rollback",
		Short: "List or restore a previous configuration revision",
		Long: `rollback inspects the config_revisions audit table populated by
apiary promote and apiary run.

Use --list to see known revisions for an environment. Use --to <digest-prefix>
to record a rollback event targeting a specific previously-recorded digest.

Note: rollback does NOT modify apiary.yaml. It records in the audit table that
an operator intends to return to a prior configuration, and prints the digest
so the operator can restore the appropriate YAML or git commit.

Examples:
  apiary rollback --env staging --list
  apiary rollback --env staging --to abc123 --note "revert to pre-deploy state"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			dbPath := filepath.Join(config.DataDir(configFile), "apiary.db")
			dbClient, err := db.New(ctx, dbPath)
			if err != nil {
				return fmt.Errorf("opening database: %w", err)
			}
			defer dbClient.Close()

			if list || toDigest == "" {
				return listRevisions(ctx, dbClient, env)
			}

			return recordRollback(ctx, dbClient, env, toDigest, note)
		},
	}

	cmd.Flags().StringVar(&env, "env", "", "environment name (empty = base config)")
	cmd.Flags().BoolVar(&list, "list", false, "list known config revisions for the environment")
	cmd.Flags().StringVar(&toDigest, "to", "", "record a rollback to this config digest (prefix match accepted)")
	cmd.Flags().StringVar(&note, "note", "", "optional operator note attached to the rollback record")
	return cmd
}

func listRevisions(ctx context.Context, dbClient *db.Client, env string) error {
	revs, err := dbClient.ListConfigRevisions(ctx, env, 20)
	if err != nil {
		return fmt.Errorf("listing revisions: %w", err)
	}
	if len(revs) == 0 {
		label := "base config"
		if env != "" {
			label = "environments." + env
		}
		fmt.Printf("No config revisions recorded for %s\n", label)
		return nil
	}

	label := "base config"
	if env != "" {
		label = "environments." + env
	}
	fmt.Printf("Config revisions for %s (newest first):\n\n", label)
	for _, r := range revs {
		fmt.Printf("  %-16s  %-12s  %s", r.ConfigDigest[:16], r.Event, r.RecordedAt.Format(time.RFC3339))
		if r.GitRevision != "" {
			fmt.Printf("  git:%s", r.GitRevision[:8])
		}
		if r.FromEnvironment != "" {
			fmt.Printf("  from:%s", r.FromEnvironment)
		}
		if r.Note != "" {
			fmt.Printf("  %q", r.Note)
		}
		fmt.Println()
	}
	return nil
}

func recordRollback(ctx context.Context, dbClient *db.Client, env, digestPrefix, note string) error {
	// Look up the target revision by digest prefix.
	revs, err := dbClient.ListConfigRevisions(ctx, env, 100)
	if err != nil {
		return fmt.Errorf("querying revisions: %w", err)
	}

	var target *db.ConfigRevision
	for i, r := range revs {
		if len(r.ConfigDigest) >= len(digestPrefix) && r.ConfigDigest[:len(digestPrefix)] == digestPrefix {
			target = &revs[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("no revision found with digest prefix %q for environment %q", digestPrefix, env)
	}

	id := fmt.Sprintf("rev_%d", time.Now().UnixNano())
	rev := &db.ConfigRevision{
		ID:           id,
		Environment:  env,
		ConfigDigest: target.ConfigDigest,
		GitRevision:  target.GitRevision,
		Event:        "rollback",
		Note:         note,
	}
	if err := dbClient.RecordConfigRevision(ctx, rev); err != nil {
		return fmt.Errorf("recording rollback: %w", err)
	}

	fmt.Printf("Rollback recorded:\n")
	fmt.Printf("  target digest:  %s\n", target.ConfigDigest)
	fmt.Printf("  originally at:  %s (%s)\n", target.RecordedAt.Format(time.RFC3339), target.Event)
	if target.GitRevision != "" {
		fmt.Printf("  git revision:   %s\n", target.GitRevision)
		fmt.Printf("\nTo restore this config, run:\n  git checkout %s -- apiary.yaml\n", target.GitRevision)
	}
	fmt.Printf("\n✓ Rollback recorded (id: %s)\n", id)
	return nil
}
