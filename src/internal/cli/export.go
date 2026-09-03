package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/orlandoburli/apiary/internal/export"
)

func newExportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export Apiary data for analysis elsewhere",
		Long: `Write Apiary's own records to files a spreadsheet or notebook can open.
Every export opens the database read-only, runs no migration, and is safe with
the daemon running.`,
	}
	cmd.AddCommand(newExportUsageCmd())
	return cmd
}

func newExportUsageCmd() *cobra.Command {
	var (
		format             string
		output             string
		since, until       string
		workflows          []string
		agents             []string
		models             []string
		sources            []string
		statuses           []string
		includeTranscripts bool
		includeSlowTools   bool
	)

	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Export per-attempt usage and cost (tokens, cost_usd, timing) with workflow context",
		Long: `Export one row per runner attempt from task_executions, joined to the workflow
instance it belongs to, so spend can be pivoted by workflow, step, model, agent
or ticket without touching the database directly.

  apiary export usage -o usage.csv
  apiary export usage --since 30d --workflow implementation --format json
  apiary export usage --since 2026-09-01 --until 2026-09-02 --status failed
  apiary export usage --include-transcripts -o full.csv

Rows are ordered oldest first. Rows that never started are excluded unless
--status pending is given. Transcript columns (input_prompt, output_text) and
slow_tools are opt-in because they dominate the file size and are not needed
for cost analysis.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			now := time.Now()
			f := export.UsageFilter{
				Workflows:          workflows,
				Agents:             agents,
				Models:             models,
				Sources:            sources,
				Statuses:           statuses,
				IncludeTranscripts: includeTranscripts,
				IncludeSlowTools:   includeSlowTools,
			}
			var err error
			if since != "" {
				if f.Since, err = export.ParseBound(since, now); err != nil {
					return fmt.Errorf("--since: %w", err)
				}
			}
			if until != "" {
				if f.Until, err = export.ParseBound(until, now); err != nil {
					return fmt.Errorf("--until: %w", err)
				}
			}
			if !f.Since.IsZero() && !f.Until.IsZero() && !f.Until.After(f.Since) {
				return fmt.Errorf("--until (%s) must be after --since (%s)",
					f.Until.Format(time.RFC3339), f.Since.Format(time.RFC3339))
			}

			dbPath := getDBPath()
			if _, err := os.Stat(dbPath); err != nil {
				return fmt.Errorf("no database at %s — has the daemon ever run?", dbPath)
			}
			db, err := export.OpenReadOnly(dbPath)
			if err != nil {
				return err
			}
			defer db.Close()

			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Minute)
			defer cancel()

			sink, commit, err := openOutput(output)
			if err != nil {
				return err
			}
			w, err := export.NewWriter(format, sink, f.Columns())
			if err != nil {
				sink.Close()
				return err
			}

			start := time.Now()
			var n int
			err = export.ListUsageRows(ctx, db, f, func(r export.Row) error {
				n++
				return w.Write(r)
			})
			if err == nil {
				err = w.Close()
			}
			if err != nil {
				sink.Close()
				return err
			}
			if err := commit(); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "exported %d rows in %s\n", n, time.Since(start).Round(time.Millisecond))
			return nil
		},
	}

	cmd.Flags().StringVar(&format, "format", "csv", "output format: csv or json")
	cmd.Flags().StringVarP(&output, "output", "o", "", "write to this file (default: stdout)")
	cmd.Flags().StringVar(&since, "since", "", "window start: duration (7d, 24h), date (2026-09-01) or RFC3339; default: all history")
	cmd.Flags().StringVar(&until, "until", "", "window end, same forms as --since; default: now")
	cmd.Flags().StringArrayVar(&workflows, "workflow", nil, "only this workflow id (repeatable)")
	cmd.Flags().StringArrayVar(&agents, "agent", nil, "only this agent id (repeatable)")
	cmd.Flags().StringArrayVar(&models, "model", nil, "only this model, exact match (repeatable)")
	cmd.Flags().StringArrayVar(&sources, "source", nil, "only this source id (repeatable)")
	cmd.Flags().StringArrayVar(&statuses, "status", nil, "only this execution status: success, failed, running, pending (repeatable)")
	cmd.Flags().BoolVar(&includeTranscripts, "include-transcripts", false, "add input_prompt and output_text columns")
	cmd.Flags().BoolVar(&includeSlowTools, "include-slow-tools", false, "add the slow_tools JSON column")
	return cmd
}

// openOutput returns the sink to write to and a commit function. With no path
// the sink is stdout and commit is a no-op. With a path the sink is a
// temporary file beside the target, renamed over it by commit, so an
// interrupted export never leaves a truncated file where a complete one is
// expected.
func openOutput(path string) (io.WriteCloser, func() error, error) {
	if path == "" {
		return nopCloser{os.Stdout}, func() error { return nil }, nil
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return nil, nil, fmt.Errorf("create %s: %w", path, err)
	}
	committed := false
	sink := &tempFile{File: tmp, onClose: func() {
		if !committed {
			os.Remove(tmp.Name())
		}
	}}
	commit := func() error {
		if err := tmp.Sync(); err != nil {
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		if err := os.Rename(tmp.Name(), path); err != nil {
			os.Remove(tmp.Name())
			return fmt.Errorf("write %s: %w", path, err)
		}
		committed = true
		return nil
	}
	return sink, commit, nil
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// tempFile removes itself on Close unless commit ran first.
type tempFile struct {
	*os.File
	onClose func()
}

func (t *tempFile) Close() error {
	err := t.File.Close()
	t.onClose()
	return err
}
