package db

import (
	"context"
	"testing"

	"github.com/orlandoburli/apiary/internal/model"
)

func sampleTiming() *model.Timing {
	return &model.Timing{
		ThinkingMS:   5_000,
		WritingMS:    20_000,
		ModelMS:      1_000,
		ToolWaitMS:   60_000,
		OtherMS:      2_000,
		BackgroundMS: 45_000,
		SlowTools: []model.ToolTiming{
			{Name: "Bash", Label: "go test ./...", DurationMS: 40_000},
			{Name: "workflow:verify", Label: "full suite", DurationMS: 30_000, Background: true},
		},
	}
}

func TestStepTimingRoundTripsThroughAStepRun(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	inst := &WorkflowInstance{ID: "wi_1", WorkflowID: "wf", State: InstanceStateRunning}
	if err := c.CreateWorkflowInstance(ctx, inst); err != nil {
		t.Fatalf("create instance: %v", err)
	}
	sr := &StepRun{
		ID: "sr_1", WorkflowInstanceID: "wi_1", StepID: "implement",
		State: StepStatePassed, StepTiming: TimingFrom(sampleTiming()),
	}
	if err := c.CreateStepRun(ctx, sr); err != nil {
		t.Fatalf("create step run: %v", err)
	}

	runs, err := c.ListStepRuns(ctx, "wi_1")
	if err != nil {
		t.Fatalf("list step runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d step runs, want 1", len(runs))
	}
	got := runs[0].StepTiming

	if got.ThinkingMS != 5_000 || got.WritingMS != 20_000 || got.ToolWaitMS != 60_000 {
		t.Errorf("buckets did not round-trip: %+v", got)
	}
	if got.BackgroundMS != 45_000 {
		t.Errorf("BackgroundMS = %d, want 45000", got.BackgroundMS)
	}
	if got.TotalMS() != 88_000 {
		t.Errorf("TotalMS = %d, want 88000 (exclusive buckets only, no background)", got.TotalMS())
	}
	tools := got.SlowToolList()
	if len(tools) != 2 || tools[0].Name != "Bash" || !tools[1].Background {
		t.Errorf("slow tools did not round-trip: %+v", tools)
	}
}

// A step run written before this data existed must be distinguishable from one
// that was measured and genuinely spent no time — otherwise the UI renders "0%
// thinking" for a step nobody measured, which reads as a finding.
func TestStepTimingReportsWhetherItWasMeasured(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	inst := &WorkflowInstance{ID: "wi_1", WorkflowID: "wf", State: InstanceStateRunning}
	if err := c.CreateWorkflowInstance(ctx, inst); err != nil {
		t.Fatalf("create instance: %v", err)
	}
	sr := &StepRun{ID: "sr_1", WorkflowInstanceID: "wi_1", StepID: "implement", State: StepStatePassed}
	if err := c.CreateStepRun(ctx, sr); err != nil {
		t.Fatalf("create step run: %v", err)
	}

	runs, err := c.ListStepRuns(ctx, "wi_1")
	if err != nil {
		t.Fatalf("list step runs: %v", err)
	}
	if runs[0].HasTiming() {
		t.Error("HasTiming() = true for a row with no timing recorded")
	}
	if got := TimingFrom(sampleTiming()); !got.HasTiming() {
		t.Error("HasTiming() = false for a measured row")
	}
}

func TestStepTimingAddSumsAttemptsAndKeepsTheWorstCalls(t *testing.T) {
	first := TimingFrom(&model.Timing{
		ThinkingMS: 1_000, ToolWaitMS: 10_000, BackgroundMS: 5_000,
		SlowTools: []model.ToolTiming{{Name: "Bash", Label: "suite", DurationMS: 9_000}},
	})
	second := TimingFrom(&model.Timing{
		ThinkingMS: 2_000, ToolWaitMS: 30_000, BackgroundMS: 7_000,
		SlowTools: []model.ToolTiming{{Name: "Bash", Label: "slower suite", DurationMS: 25_000}},
	})
	first.Add(second)

	if first.ThinkingMS != 3_000 || first.ToolWaitMS != 40_000 {
		t.Errorf("buckets = %+v, want summed across attempts", first)
	}
	// Attempts are sequential, so their background intervals cannot overlap.
	if first.BackgroundMS != 12_000 {
		t.Errorf("BackgroundMS = %d, want 12000", first.BackgroundMS)
	}
	tools := first.SlowToolList()
	if len(tools) != 2 {
		t.Fatalf("slow tools = %+v, want both attempts' calls", tools)
	}
	// The failed attempt's 25s call is the worst thing that happened to this step,
	// so it must lead — reporting only the winning attempt would hide it.
	if tools[0].DurationMS != 25_000 {
		t.Errorf("slow tools not re-ranked across attempts: %+v", tools)
	}
}

func TestStepTimingAddBoundsThePersistedList(t *testing.T) {
	var total StepTiming
	for i := 0; i < maxPersistedSlowTools+4; i++ {
		total.Add(TimingFrom(&model.Timing{
			SlowTools: []model.ToolTiming{{Name: "Bash", DurationMS: int64(1_000 * (i + 1))}},
		}))
	}
	if got := len(total.SlowToolList()); got != maxPersistedSlowTools {
		t.Errorf("kept %d entries after many attempts, want %d", got, maxPersistedSlowTools)
	}
}

// Timing is diagnostic: an unreadable blob must degrade to "no detail" rather
// than propagate an error into a caller that only wanted to render a row.
func TestStepTimingToleratesAnUnreadableBlob(t *testing.T) {
	timing := StepTiming{SlowTools: "{not json"}
	if got := timing.SlowToolList(); got != nil {
		t.Errorf("SlowToolList = %+v, want nil", got)
	}
}
