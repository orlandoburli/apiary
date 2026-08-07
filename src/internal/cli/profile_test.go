package cli

import (
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/daemon"
	"github.com/orlandoburli/apiary/internal/model"
)

func TestBuildProfileTotalsStepsAndRanksCallsAcrossThem(t *testing.T) {
	detail := daemon.InstanceDetail{
		InstanceSummary: daemon.InstanceSummary{ID: "wi_1", Workflow: "implement"},
		Steps: []daemon.StepRunView{
			{
				StepID: "plan",
				Timing: &model.Timing{
					ThinkingMS: 60_000, WritingMS: 30_000, TotalMS: 90_000,
					SlowTools: []model.ToolTiming{{Name: "Read", DurationMS: 4_000}},
				},
			},
			{
				StepID: "implement",
				Timing: &model.Timing{
					ThinkingMS: 10_000, WritingMS: 20_000, ToolWaitMS: 570_000,
					BackgroundMS: 500_000, TotalMS: 600_000,
					SlowTools: []model.ToolTiming{
						{Name: "Bash", Label: "full suite", DurationMS: 400_000, Background: true},
					},
				},
			},
			// A step from before attribution existed, mixed in with measured ones.
			{StepID: "review"},
		},
	}

	got := buildProfile(detail)

	if got.Total.TotalMS != 690_000 {
		t.Errorf("Total.TotalMS = %d, want 690000", got.Total.TotalMS)
	}
	if got.Total.ThinkingMS != 70_000 || got.Total.ToolWaitMS != 570_000 {
		t.Errorf("totals = %+v", got.Total)
	}
	if got.Total.BackgroundMS != 500_000 {
		t.Errorf("Total.BackgroundMS = %d, want 500000", got.Total.BackgroundMS)
	}
	if len(got.Steps) != 3 {
		t.Fatalf("got %d steps, want 3 (including the unmeasured one)", len(got.Steps))
	}
	if got.Steps[2].Timing != nil {
		t.Error("unmeasured step should carry nil timing, not a zeroed breakdown")
	}
	if len(got.SlowestCalls) != 2 {
		t.Fatalf("SlowestCalls = %+v, want 2", got.SlowestCalls)
	}
	// The list is what to go and fix, so the worst call leads regardless of which
	// step it came from.
	if got.SlowestCalls[0].StepID != "implement" || got.SlowestCalls[0].DurationMS != 400_000 {
		t.Errorf("slowest call = %+v, want the implement step's 400s suite", got.SlowestCalls[0])
	}
}

func TestBuildProfileHandlesARunWithNoTiming(t *testing.T) {
	detail := daemon.InstanceDetail{
		InstanceSummary: daemon.InstanceSummary{ID: "wi_1"},
		Steps:           []daemon.StepRunView{{StepID: "plan"}, {StepID: "implement"}},
	}
	got := buildProfile(detail)

	if got.Total.TotalMS != 0 {
		t.Errorf("Total.TotalMS = %d, want 0", got.Total.TotalMS)
	}
	if len(got.SlowestCalls) != 0 {
		t.Errorf("SlowestCalls = %+v, want none", got.SlowestCalls)
	}
}

func TestTimingSummaryOmitsUnmeasuredSteps(t *testing.T) {
	if got := timingSummary(nil); got != "" {
		t.Errorf("timingSummary(nil) = %q, want empty", got)
	}
	if got := timingSummary(&model.Timing{}); got != "" {
		t.Errorf("timingSummary(zero) = %q, want empty", got)
	}
}

func TestTimingSummaryReportsSharesAndFlagsUnsplitLatency(t *testing.T) {
	got := timingSummary(&model.Timing{
		ThinkingMS: 250, WritingMS: 250, ToolWaitMS: 250, ModelMS: 250, TotalMS: 1_000,
	})
	for _, want := range []string{"think 25%", "write 25%", "tools 25%", "unsplit 25%"} {
		if !strings.Contains(got, want) {
			t.Errorf("timingSummary = %q, missing %q", got, want)
		}
	}
}

func TestShareCellAndDurationCell(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"zero share reads as absent, not as 0%", shareCell(0, 1_000), "—"},
		{"share of a total", shareCell(625, 1_000), "62%"},
		{"share with no total falls back to the duration", shareCell(5_000, 0), "5s"},
		{"sub-minute duration", durationCell(4_500), "4.5s"},
		{"long duration rounds to seconds", durationCell(4_920_000), "1h22m0s"},
		{"no duration", durationCell(0), "—"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("got %q, want %q", tc.got, tc.want)
			}
		})
	}
}
