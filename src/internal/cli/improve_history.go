package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/orlandoburli/apiary/internal/improve"
)

func newImproveHistoryCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "history",
		Short: "List past improvement runs",
		RunE: func(cmd *cobra.Command, args []string) error {
			ledger, err := openLedger(cmd.Context())
			if err != nil {
				return err
			}
			defer ledger.Close()

			runs, err := ledger.ListRuns(cmd.Context(), limit)
			if err != nil {
				return err
			}
			if len(runs) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no improvement runs recorded yet")
				return nil
			}

			fmt.Fprintln(cmd.OutOrStdout(),
				instHeader.Render("RUN                       WHEN              EFFORT    ADVISOR              APPLIED   COST"))
			for _, r := range runs {
				applied := "—"
				if r.Applied && r.AppliedAt != nil {
					applied = r.AppliedAt.Local().Format("01-02 15:04")
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%-25s %-17s %-9s %-20s %-9s $%.2f\n",
					r.ID, r.CreatedAt.Local().Format("2006-01-02 15:04"), r.Effort,
					truncateStr(r.AdvisorAgent+"/"+r.AdvisorModel, 20), applied, r.CostUSD)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20, "how many runs to list")
	return cmd
}

func newImproveShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <run-id>",
		Short: "Show a past improvement run's findings",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ledger, err := openLedger(cmd.Context())
			if err != nil {
				return err
			}
			defer ledger.Close()

			run, findings, err := ledger.GetRun(cmd.Context(), args[0])
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "# Improvement run %s\n\n", run.ID)
			fmt.Fprintf(out, "%s → %s · effort %s · advisor %s (%s / %s)\n",
				run.WindowStart.Format(time.DateOnly), run.WindowEnd.Format(time.DateOnly),
				run.Effort, run.AdvisorAgent, run.AdvisorRunner, run.AdvisorModel)
			fmt.Fprintf(out, "recorded %s · %d tokens · $%.4f\n",
				run.CreatedAt.Local().Format(time.RFC3339), run.TotalTokens, run.CostUSD)
			if run.Applied && run.AppliedAt != nil {
				fmt.Fprintf(out, "applied %s\n", run.AppliedAt.Local().Format(time.RFC3339))
			} else {
				fmt.Fprintln(out, "not applied")
			}
			if run.ReportPath != "" {
				fmt.Fprintf(out, "artifacts: %s\n", run.ReportPath)
			}
			fmt.Fprintln(out)

			for _, f := range findings {
				fmt.Fprintf(out, "## [%s] %s\n", strings.ToUpper(orDash(f.State)), orDash(f.Symptom))
				fmt.Fprintf(out, "- scope: %s\n", orDash(f.Scope))
				if f.TargetFile != "" {
					fmt.Fprintf(out, "- file: %s\n", f.TargetFile)
				}
				if f.Confidence != "" {
					fmt.Fprintf(out, "- confidence: %s\n", f.Confidence)
				}
				if !f.MachineChecked && f.Patch != "" {
					fmt.Fprintln(out, "- not machine-checked (instruction file)")
				}
				if f.RejectReason != "" {
					fmt.Fprintf(out, "- rejected: %s\n", f.RejectReason)
				}
				if f.Rationale != "" {
					fmt.Fprintf(out, "\n%s\n", f.Rationale)
				}
				fmt.Fprintln(out)
			}
			return nil
		},
	}
}

func newImproveEffectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "effect <run-id>",
		Short: "Compare metrics before and after an applied run",
		Long: `Recompute the metrics behind each applied finding over the window since it was
applied, and compare them to the baseline captured when it was proposed.

This is what makes the advisor a loop rather than a report generator. It matters
most for instruction changes: nothing can validate a soul file mechanically, so
measured effect is the only evidence that such a change did what it claimed.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ledger, err := openLedger(cmd.Context())
			if err != nil {
				return err
			}
			defer ledger.Close()

			run, findings, err := ledger.GetRun(cmd.Context(), args[0])
			if err != nil {
				return err
			}

			db, err := improve.OpenReadOnly(getDBPath())
			if err != nil {
				return err
			}
			defer db.Close()

			effects, err := improve.MeasureEffect(cmd.Context(), db, run, findings, time.Now())
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), improve.RenderEffect(run, effects))
			return nil
		},
	}
}

func openLedger(ctx context.Context) (*improve.Ledger, error) {
	dbPath := getDBPath()
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("no database at %s — has the daemon ever run?", dbPath)
	}
	return improve.OpenLedger(ctx, dbPath)
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
