package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/improve"
)

func newImproveCmd() *cobra.Command {
	var (
		since         string
		workflows     []string
		agents        []string
		focus         string
		dumpEvidence  bool
		hotspots      int
		transcripts   int
		excerptBudget int
	)

	cmd := &cobra.Command{
		Use:   "improve",
		Short: "Analyse execution history and suggest improvements",
		Long: `Mine Apiary's own execution history — step timings, tokens, cost, failure
kinds, rework loops, wait polling — into an evidence pack describing how the
configured workflows and agents actually behave.

  apiary improve --dump-evidence            print the evidence pack as JSON
  apiary improve --dump-evidence --since 30d
  apiary improve --dump-evidence --workflow review-pr

The evidence pack is computed entirely in Go: no model is consulted, so the same
database and window always produce the same pack. The advisor that reasons over
it and proposes config changes arrives in a later phase.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !dumpEvidence {
				return fmt.Errorf("only --dump-evidence is available so far; the advisor lands in a later phase")
			}

			window, err := improve.ParseWindow(since, time.Now())
			if err != nil {
				return err
			}

			// The config is optional: without it the pack still reports what ran,
			// it just cannot report what was configured and never ran.
			var cfg *config.Config
			if loaded, err := config.Load(configFile); err == nil {
				cfg = loaded
			} else {
				fmt.Fprintf(os.Stderr, "  ⚠ config not loaded (%v); dead-path and parallel analysis skipped\n", err)
			}

			dbPath := getDBPath()
			if _, err := os.Stat(dbPath); err != nil {
				return fmt.Errorf("no database at %s — has the daemon ever run?", dbPath)
			}
			db, err := improve.OpenReadOnly(dbPath)
			if err != nil {
				return err
			}
			defer db.Close()

			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()

			pack, err := improve.Build(ctx, db, improve.Options{
				DBPath: dbPath,
				LogDir: getLogDir(),
				Config: cfg,
				Window: window,
				Scope: improve.Scope{
					Workflows: workflows,
					Agents:    agents,
					Focus:     improve.Focus(focus),
				},
				HotspotLimit:          hotspots,
				TranscriptsPerHotspot: transcripts,
				TranscriptByteBudget:  excerptBudget,
			})
			if err != nil {
				return err
			}

			fmt.Fprintln(os.Stderr, pack.Summary())
			fmt.Fprintln(os.Stderr)

			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(pack)
		},
	}

	cmd.Flags().StringVar(&since, "since", "14d", "history window (e.g. 7d, 24h, 90d)")
	cmd.Flags().StringSliceVar(&workflows, "workflow", nil, "restrict analysis to these workflow ids")
	cmd.Flags().StringSliceVar(&agents, "agent", nil, "restrict analysis to these agents' runs")
	cmd.Flags().StringVar(&focus, "focus", "all", "what to optimise for: cost|latency|reliability|quality|all")
	cmd.Flags().BoolVar(&dumpEvidence, "dump-evidence", false, "print the evidence pack as JSON and exit")
	cmd.Flags().IntVar(&hotspots, "hotspots", 0, "number of hotspot steps to sample transcripts for")
	cmd.Flags().IntVar(&transcripts, "transcripts", 0, "transcripts to read per hotspot (0 disables sampling)")
	cmd.Flags().IntVar(&excerptBudget, "transcript-bytes", 8000, "per-transcript character budget")

	return cmd
}
