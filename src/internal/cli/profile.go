package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/orlandoburli/apiary/internal/daemon"
	"github.com/orlandoburli/apiary/internal/model"
)

// `apiary profile` answers "where did this run's hours go?" (issue #399).
//
// Agent steps here routinely run 45–90 minutes, and until the wall clock was
// attributed the only way to find out what a long step had been doing was to
// hand-parse the daemon log with a throwaway script. The diagnosis that motivated
// this — a step that was 62% blocked on background tasks it had launched and only
// 6.5% thinking — pointed at three concrete fixes; the tuning everyone assumed was
// needed would have addressed the 6.5%.
//
// --json exists so the next question nobody anticipated can be answered with jq
// instead of another log parser.
func newProfileCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "profile <instance-id>",
		Short: "Show where a workflow run's wall clock went",
		Long: "Break a run's wall clock down per step — thinking, writing, tool waits — " +
			"and list the slowest individual calls, so a long step can be diagnosed " +
			"instead of guessed at.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return showProfile(args[0], asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output as JSON")
	return cmd
}

// ProfileStep is one step's timing in the --json payload. It is a projection of
// the instance detail rather than a new endpoint: the daemon already serves the
// timing on the step rows, and a second source for the same numbers is a second
// thing to keep in agreement.
type ProfileStep struct {
	StepID  string        `json:"step_id"`
	AgentID string        `json:"agent_id"`
	State   string        `json:"state"`
	Timing  *model.Timing `json:"timing"`
}

// ProfileReport is the --json payload.
type ProfileReport struct {
	InstanceID string        `json:"instance_id"`
	Workflow   string        `json:"workflow"`
	Steps      []ProfileStep `json:"steps"`
	Total      model.Timing  `json:"total"`
	// SlowestCalls is the worst calls across every step, each tagged with the step
	// it came from — the "what should I actually fix" list.
	SlowestCalls []ProfileCall `json:"slowest_calls"`
}

// ProfileCall is one entry in the cross-step slowest-calls list.
type ProfileCall struct {
	StepID string `json:"step_id"`
	model.ToolTiming
}

// profileSlowestCalls caps the cross-step list. Long enough to cover the handful
// of calls that explain a long run, short enough to still be a shortlist.
const profileSlowestCalls = 10

func showProfile(id string, asJSON bool) error {
	var detail daemon.InstanceDetail
	if err := ipcGetJSON("/instances/"+url.PathEscape(id), &detail); err != nil {
		if strings.Contains(err.Error(), "not found") {
			fmt.Println(instErr.Render("Instance not found: ") + id)
			os.Exit(2)
		}
		return daemonDownHint()
	}

	report := buildProfile(detail)
	if asJSON {
		line, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(line))
		return nil
	}
	printProfile(detail, report)
	return nil
}

func buildProfile(detail daemon.InstanceDetail) ProfileReport {
	report := ProfileReport{InstanceID: detail.ID, Workflow: detail.Workflow}
	var calls []ProfileCall
	for _, s := range detail.Steps {
		report.Steps = append(report.Steps, ProfileStep{
			StepID: s.StepID, AgentID: s.AgentID, State: s.State, Timing: s.Timing,
		})
		if s.Timing == nil {
			continue
		}
		report.Total.ThinkingMS += s.Timing.ThinkingMS
		report.Total.WritingMS += s.Timing.WritingMS
		report.Total.ModelMS += s.Timing.ModelMS
		report.Total.ToolWaitMS += s.Timing.ToolWaitMS
		report.Total.OtherMS += s.Timing.OtherMS
		// Steps run one after another, so their background intervals cannot
		// overlap and summing them is safe at this level.
		report.Total.BackgroundMS += s.Timing.BackgroundMS
		report.Total.TotalMS += s.Timing.TotalMS
		for _, c := range s.Timing.SlowTools {
			calls = append(calls, ProfileCall{StepID: s.StepID, ToolTiming: c})
		}
	}
	sort.SliceStable(calls, func(i, j int) bool { return calls[i].DurationMS > calls[j].DurationMS })
	if len(calls) > profileSlowestCalls {
		calls = calls[:profileSlowestCalls]
	}
	report.SlowestCalls = calls
	return report
}

func printProfile(detail daemon.InstanceDetail, report ProfileReport) {
	cell := detail.CellID
	if detail.Title != "" {
		cell += " — " + detail.Title
	}
	fmt.Printf("%s  %s\n", instMuted.Render("Instance:"), detail.ID)
	fmt.Printf("%s  %s\n", instMuted.Render("Workflow:"), detail.Workflow)
	fmt.Printf("%s      %s\n\n", instMuted.Render("Cell:"), cell)

	measured := 0
	for _, s := range report.Steps {
		if s.Timing != nil {
			measured++
		}
	}
	if measured == 0 {
		fmt.Println(instMuted.Render("No timing recorded for this run."))
		fmt.Println(instMuted.Render("Steps that ran before wall-clock attribution existed carry none, " +
			"as do runners that stream no events."))
		return
	}

	fmt.Println(instHeader.Render(fmt.Sprintf("%-18s %-9s %-9s %-9s %-9s %-9s", "STEP", "TOTAL", "THINK", "WRITE", "TOOLS", "OTHER")))
	for _, s := range report.Steps {
		if s.Timing == nil {
			fmt.Printf("%-18s %s\n", truncate(s.StepID, 18), instMuted.Render("not measured"))
			continue
		}
		fmt.Printf("%-18s %-9s %-9s %-9s %-9s %-9s\n",
			truncate(s.StepID, 18),
			durationCell(s.Timing.TotalMS),
			shareCell(s.Timing.ThinkingMS, s.Timing.TotalMS),
			shareCell(s.Timing.WritingMS, s.Timing.TotalMS),
			shareCell(s.Timing.ToolWaitMS, s.Timing.TotalMS),
			shareCell(s.Timing.OtherMS, s.Timing.TotalMS),
		)
		// Un-attributed model latency is called out rather than folded into
		// thinking or writing, so the breakdown never overstates what it knows.
		if s.Timing.ModelMS > 0 {
			fmt.Printf("  %s\n", instMuted.Render(fmt.Sprintf("%s model latency with no thinking signal to split on",
				shareCell(s.Timing.ModelMS, s.Timing.TotalMS))))
		}
		if s.Timing.BackgroundMS > 0 {
			fmt.Printf("  %s\n", instMuted.Render(fmt.Sprintf("%s with background work outstanding (overlaps the above)",
				shareCell(s.Timing.BackgroundMS, s.Timing.TotalMS))))
		}
	}

	fmt.Printf("\n%-18s %-9s %-9s %-9s %-9s %-9s\n",
		instHeader.Render("TOTAL"),
		durationCell(report.Total.TotalMS),
		shareCell(report.Total.ThinkingMS, report.Total.TotalMS),
		shareCell(report.Total.WritingMS, report.Total.TotalMS),
		shareCell(report.Total.ToolWaitMS, report.Total.TotalMS),
		shareCell(report.Total.OtherMS, report.Total.TotalMS),
	)

	if len(report.SlowestCalls) == 0 {
		return
	}
	fmt.Println()
	fmt.Println(instHeader.Render("Slowest calls"))
	for _, c := range report.SlowestCalls {
		kind := "tool"
		if c.Background {
			kind = "background"
		}
		line := fmt.Sprintf("  %-9s  %-10s  %-14s  %s",
			durationCell(c.DurationMS), kind, truncate(c.StepID, 14), c.Name)
		if c.Label != "" {
			line += instMuted.Render("  ·  " + truncate(c.Label, 60))
		}
		fmt.Println(line)
	}
}

// timingSummary renders a step's attribution as one line for `apiary instances`.
// Empty when the step carries no timing, so an unmeasured step prints nothing
// rather than a row of zeros.
func timingSummary(t *model.Timing) string {
	if t == nil || t.TotalMS == 0 {
		return ""
	}
	parts := []string{
		"think " + shareCell(t.ThinkingMS, t.TotalMS),
		"write " + shareCell(t.WritingMS, t.TotalMS),
		"tools " + shareCell(t.ToolWaitMS, t.TotalMS),
	}
	if t.ModelMS > 0 {
		parts = append(parts, "unsplit "+shareCell(t.ModelMS, t.TotalMS))
	}
	if t.BackgroundMS > 0 {
		parts = append(parts, "background "+shareCell(t.BackgroundMS, t.TotalMS))
	}
	return strings.Join(parts, "  ·  ")
}

// durationCell renders a millisecond count as a compact human duration.
func durationCell(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	d := time.Duration(ms) * time.Millisecond
	if d < time.Minute {
		return d.Round(100 * time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}

// shareCell renders a bucket as a percentage of the total. Percentages are what
// make a breakdown actionable — "19.6 minutes writing" only means something once
// you know it was a quarter of the step.
func shareCell(ms, total int64) string {
	if ms <= 0 {
		return "—"
	}
	if total <= 0 {
		return durationCell(ms)
	}
	return fmt.Sprintf("%.0f%%", float64(ms)/float64(total)*100)
}
