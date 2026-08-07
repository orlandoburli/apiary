package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
		effortFlag    string
		advisorID     string
		runnerID      string
		modelID       string
		profile       string
		output        string
		outDir        string
		dumpEvidence  bool
		dumpPrompt    bool
		apply         bool
		assumeYes     bool
		excerptBudget int
	)

	cmd := &cobra.Command{
		Use:   "improve",
		Short: "Analyse execution history and suggest improvements",
		Long: `Mine Apiary's own execution history — step timings, tokens, cost, failure
kinds, rework loops, wait polling — and have an agent reason over it, proposing
changes to the configuration that produced it.

  apiary improve                          analyse and print findings
  apiary improve --effort deep --since 30d
  apiary improve --workflow implementation
  apiary improve --dump-evidence          print the metrics as JSON, run no model

The evidence is computed entirely in Go, so --dump-evidence needs no advisor and
no model. The advisor is a normal Apiary agent, resolved from --advisor, an
ad-hoc --runner/--model pair, settings.improve.agent, or an agent named
"improver".`,
		RunE: func(cmd *cobra.Command, args []string) error {
			effort, err := improve.ParseEffort(effortFlag)
			if err != nil {
				return err
			}
			knobs := effort.Expand()
			if excerptBudget > 0 {
				knobs.TranscriptByteBudget = excerptBudget
			}

			// --since defaults per effort: a quick pass over 90 days is a waste,
			// and a deep pass over 7 days usually has too little to chew on.
			if since == "" {
				since = knobs.DefaultWindow
			}
			window, err := improve.ParseWindow(since, time.Now())
			if err != nil {
				return err
			}

			cfg, cfgErr := config.Load(configFile)
			if cfgErr != nil && !dumpEvidence {
				return fmt.Errorf("loading config: %w", cfgErr)
			}
			if cfgErr != nil {
				fmt.Fprintf(os.Stderr, "  ⚠ config not loaded (%v); dead-path and parallel analysis skipped\n", cfgErr)
			}
			if cfg != nil && profile != "" {
				applied, found := config.ApplyProfile(cfg, profile)
				if !found {
					fmt.Fprintf(os.Stderr, "  ⚠ profile %q not found — continuing with base config\n", profile)
				} else {
					fmt.Fprintf(os.Stderr, "  active profile: %s (%d agent overrides)\n", profile, applied)
				}
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

			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Minute)
			defer cancel()

			scope := improve.Scope{Workflows: workflows, Agents: agents, Focus: improve.Focus(focus)}
			pack, err := improve.Build(ctx, db, improve.Options{
				DBPath:                dbPath,
				LogDir:                getLogDir(),
				Config:                cfg,
				Window:                window,
				Scope:                 scope,
				HotspotLimit:          knobs.HotspotLimit,
				TranscriptsPerHotspot: knobs.TranscriptsPerHotspot,
				TranscriptByteBudget:  knobs.TranscriptByteBudget,
			})
			if err != nil {
				return err
			}

			if dumpEvidence {
				fmt.Fprintln(os.Stderr, pack.Summary())
				fmt.Fprintln(os.Stderr)
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(pack)
			}

			ws, err := improve.Discover(cfg, configFile)
			if err != nil {
				return err
			}
			files := ws.Filter(knobs.WorkspaceBreadth, activeAgents(pack), flaggedAgents(pack))
			prompt := improve.ComposePrompt(pack, ws, files, knobs)

			// --dump-prompt runs no model, so it needs no advisor. It exists so the
			// exact text that would be sent can be reviewed first — including
			// confirming that the config reached it redacted.
			if dumpPrompt {
				fmt.Fprint(cmd.OutOrStdout(), prompt)
				return nil
			}

			adv, err := improve.ResolveAdvisor(cfg, improve.AdvisorFlags{
				Advisor: advisorID, Runner: runnerID, Model: modelID, Effort: effort,
			})
			if err != nil {
				return err
			}

			fmt.Fprintln(os.Stderr, pack.Summary())
			fmt.Fprintf(os.Stderr, "\nanalysing with %s (%s / %s), effort %s, %d config files, %d transcripts…\n",
				adv.AgentID, adv.RunnerID, adv.Model, effort, len(files), len(pack.Transcripts))
			for _, s := range ws.UnresolvedSkills {
				fmt.Fprintf(os.Stderr, "  ⚠ skill not found on disk: %s\n", s)
			}

			outcome, runErr := improve.RunAdvisor(ctx, cfg, adv, prompt, knobs, filepath.Dir(configFile))
			if runErr != nil {
				if outcome != nil && outcome.RawOutput != "" {
					fmt.Fprintf(os.Stderr, "\n--- advisor raw output ---\n%s\n", outcome.RawOutput)
				}
				return runErr
			}

			// Validate every proposal before it is shown. A patch that cannot be
			// applied, or that breaks the config, must not reach the reviewer
			// looking actionable.
			validator := improve.NewValidator(ws, configFile, cfg)
			verdicts := validator.Validate(outcome.Analysis.Recommendations)

			if knobs.Critic {
				fmt.Fprintf(os.Stderr, "running critic pass over %d proposal(s)…\n", len(verdicts))
				updated, criticOut, err := improve.RunCritic(ctx, cfg, adv, outcome.Analysis, verdicts,
					files, knobs, filepath.Dir(configFile))
				if err != nil {
					// A failed critic must not discard a valid analysis.
					fmt.Fprintf(os.Stderr, "  ⚠ critic pass failed (%v); proposals are shown unreviewed\n", err)
				}
				verdicts = updated
				if criticOut != nil {
					outcome.AddUsage(criticOut.Usage)
				}
			}

			report := improve.RenderReport(pack, adv, outcome, effort)
			diff := improve.RenderDiff(outcome.Analysis, verdicts)

			if outDir != "" {
				if err := writeArtifacts(outDir, pack, outcome, report+"\n"+diff); err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "written to %s\n", outDir)
			}

			fmt.Fprintln(os.Stderr, improve.DiffSummary(verdicts))

			if apply {
				return applyChanges(cmd, ws, verdicts, diff, assumeYes)
			}

			switch output {
			case "json":
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(outcome.Analysis)
			case "report":
				fmt.Fprint(cmd.OutOrStdout(), report)
				return nil
			default: // diff
				fmt.Fprint(cmd.OutOrStdout(), report)
				fmt.Fprint(cmd.OutOrStdout(), "\n"+diff)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&since, "since", "", "history window (e.g. 7d, 24h, 90d); defaults per effort")
	cmd.Flags().StringSliceVar(&workflows, "workflow", nil, "restrict analysis to these workflow ids")
	cmd.Flags().StringSliceVar(&agents, "agent", nil, "restrict analysis to these agents' runs")
	cmd.Flags().StringVar(&focus, "focus", "all", "what to optimise for: cost|latency|reliability|quality|all")
	cmd.Flags().StringVar(&effortFlag, "effort", "standard", "depth of analysis: quick|standard|deep")
	cmd.Flags().StringVar(&advisorID, "advisor", "", "agent that performs the analysis")
	cmd.Flags().StringVar(&runnerID, "runner", "", "ad-hoc runner for the analysis (requires --model)")
	cmd.Flags().StringVar(&modelID, "model", "", "ad-hoc model for the analysis (requires --runner)")
	cmd.Flags().StringVar(&profile, "profile", "", "activate a named runner profile from config profiles.<name>")
	cmd.Flags().StringVar(&output, "output", "diff", "what to print: diff|report|json")
	cmd.Flags().StringVar(&outDir, "out", "", "also write report, analysis and evidence to this directory")
	cmd.Flags().BoolVar(&dumpEvidence, "dump-evidence", false, "print the evidence pack as JSON and exit (runs no model)")
	cmd.Flags().BoolVar(&dumpPrompt, "dump-prompt", false, "print the composed advisor prompt and exit (runs no model)")
	cmd.Flags().BoolVar(&apply, "apply", false, "write the accepted changes to disk (workspace is assumed to be under version control)")
	cmd.Flags().BoolVar(&assumeYes, "yes", false, "skip the confirmation prompt when applying")
	cmd.Flags().IntVar(&excerptBudget, "transcript-bytes", 0, "override the per-transcript character budget")

	return cmd
}

// activeAgents are the agents that ran at all in the window.
func activeAgents(pack *improve.EvidencePack) map[string]bool {
	out := map[string]bool{}
	for _, a := range pack.Agents {
		out[a.AgentID] = true
	}
	return out
}

// flaggedAgents are the agents attached to a step that looks worth explaining:
// it fails, it gets truncated at the turn cap, or it fails over. At quick effort
// only these agents' instructions are worth the tokens.
func flaggedAgents(pack *improve.EvidencePack) map[string]bool {
	out := map[string]bool{}
	for _, s := range pack.Steps {
		if s.AgentID == "" {
			continue
		}
		if s.FailRate > 0 || s.MaxTurnsSaturation > 0 || s.FailoverRate > 0 {
			out[s.AgentID] = true
		}
	}
	for _, w := range pack.Workflows {
		if len(w.ReworkLoops) == 0 {
			continue
		}
		for _, s := range pack.Steps {
			for _, l := range w.ReworkLoops {
				if s.WorkflowID == w.WorkflowID && s.StepID == l.StepID && s.AgentID != "" {
					out[s.AgentID] = true
				}
			}
		}
	}
	return out
}

func writeArtifacts(dir string, pack *improve.EvidencePack, outcome *improve.RunOutcome, report string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}
	write := func(name string, v any) error {
		raw, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, name), raw, 0o600)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte(report), 0o600); err != nil {
		return err
	}
	if err := write("evidence.json", pack); err != nil {
		return err
	}
	return write("analysis.json", outcome.Analysis)
}

// applyChanges shows the diff, asks once, and writes. The diff is printed in
// full first: a confirmation prompt for changes the operator has not seen is
// not consent, it is a formality.
func applyChanges(cmd *cobra.Command, ws *improve.Workspace, verdicts []improve.Verdict, diff string, assumeYes bool) error {
	prompt := improve.ConfirmationPrompt(verdicts)
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "nothing to apply — no proposal survived validation")
		return nil
	}

	fmt.Fprint(cmd.OutOrStdout(), diff)

	if !assumeYes {
		if !improve.IsGitRepo(ws.Root) {
			fmt.Fprintln(os.Stderr, "\n⚠ This workspace is not a git repository. These edits are written in place")
			fmt.Fprintln(os.Stderr, "  and there will be no automatic way back.")
		}
		fmt.Fprint(os.Stderr, "\n"+prompt)
		var answer string
		_, _ = fmt.Fscanln(cmd.InOrStdin(), &answer)
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "y", "yes":
		default:
			fmt.Fprintln(os.Stderr, "aborted; nothing written")
			return nil
		}
	}

	res, err := improve.Apply(verdicts, ws.Root)
	if err != nil {
		return err
	}
	fmt.Fprint(os.Stderr, "\n"+res.Summary())
	return nil
}
