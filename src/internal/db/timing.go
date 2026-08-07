package db

import (
	"encoding/json"

	"github.com/orlandoburli/apiary/internal/model"
)

// StepTiming is the persisted wall-clock attribution for a step or a single
// runner attempt (issue #399): where the minutes went, alongside the token and
// cost columns that already sit next to it.
//
// It is embedded in both Execution (one runner invocation) and StepRun (the
// logical step, summed across that step's failover attempts), so the two carry
// the same shape and the rollup is a plain field-wise addition — the same
// treatment the token columns get.
//
// ThinkingMS, WritingMS, ModelMS, ToolWaitMS and OtherMS are exclusive: each
// instant of wall clock lands in exactly one of them. BackgroundMS is NOT one of
// them — it is the union of the intervals during which background work was
// outstanding, which overlaps the others by design, so adding it in would
// double-count. See model.Timing for how the attribution is derived.
type StepTiming struct {
	ThinkingMS   int64
	WritingMS    int64
	ModelMS      int64
	ToolWaitMS   int64
	OtherMS      int64
	BackgroundMS int64
	// SlowTools is the JSON-encoded slowest-calls list (see model.ToolTiming).
	// Stored as JSON rather than as columns because it is a variable-length list
	// nothing aggregates over — the buckets above are what queries sum.
	SlowTools string
}

// TotalMS is the run's attributed wall clock: the exclusive buckets only.
// BackgroundMS is excluded deliberately; it overlaps them.
func (t StepTiming) TotalMS() int64 {
	return t.ThinkingMS + t.WritingMS + t.ModelMS + t.ToolWaitMS + t.OtherMS
}

// HasTiming reports whether any wall-clock attribution was recorded. Rows written
// before this data existed leave every bucket at zero, and callers must tell that
// apart from a genuine measurement — a step rendered as "0% thinking" reads as a
// finding when it actually means "not measured".
func (t StepTiming) HasTiming() bool {
	return t.TotalMS() > 0 || t.BackgroundMS > 0
}

// SlowToolList decodes the persisted slowest-calls list, returning nil when the
// row carries none or the payload is unreadable. Timing data is diagnostic, so a
// malformed blob degrades to "no detail" rather than failing the caller.
func (t StepTiming) SlowToolList() []model.ToolTiming {
	if t.SlowTools == "" {
		return nil
	}
	var out []model.ToolTiming
	if err := json.Unmarshal([]byte(t.SlowTools), &out); err != nil {
		return nil
	}
	return out
}

// TimingFrom converts a runner's reported timing into its persisted form.
// Returns the zero value for runners that report none (the API-based runners,
// which have no event stream to attribute).
func TimingFrom(t *model.Timing) StepTiming {
	if t == nil {
		return StepTiming{}
	}
	out := StepTiming{
		ThinkingMS:   t.ThinkingMS,
		WritingMS:    t.WritingMS,
		ModelMS:      t.ModelMS,
		ToolWaitMS:   t.ToolWaitMS,
		OtherMS:      t.OtherMS,
		BackgroundMS: t.BackgroundMS,
	}
	if len(t.SlowTools) > 0 {
		if raw, err := json.Marshal(t.SlowTools); err == nil {
			out.SlowTools = string(raw)
		}
	}
	return out
}

// ToModel converts a persisted rollup back into the runner-facing shape, so a
// step-level total can travel the same path a single run's timing does. TotalMS
// is recomputed rather than stored — it is always the sum of the exclusive
// buckets, and deriving it removes any chance of a row whose total disagrees with
// its own parts.
func (t StepTiming) ToModel() model.Timing {
	return model.Timing{
		ThinkingMS:   t.ThinkingMS,
		WritingMS:    t.WritingMS,
		ModelMS:      t.ModelMS,
		ToolWaitMS:   t.ToolWaitMS,
		OtherMS:      t.OtherMS,
		BackgroundMS: t.BackgroundMS,
		TotalMS:      t.TotalMS(),
		SlowTools:    t.SlowToolList(),
	}
}

// Add folds one attempt's timing into a step-level rollup, mirroring how the
// token columns are summed across a step's failover attempts. The slowest-call
// lists are merged and re-ranked so the step reports the worst calls across every
// attempt, not just the winning one — a step that burned an hour in a failed
// attempt should say so.
func (t *StepTiming) Add(other StepTiming) {
	t.ThinkingMS += other.ThinkingMS
	t.WritingMS += other.WritingMS
	t.ModelMS += other.ModelMS
	t.ToolWaitMS += other.ToolWaitMS
	t.OtherMS += other.OtherMS
	// Attempts run one after another, never concurrently, so their background
	// intervals cannot overlap and summing is safe here (unlike within one run,
	// where the union is taken).
	t.BackgroundMS += other.BackgroundMS

	merged := append(t.SlowToolList(), other.SlowToolList()...)
	if len(merged) == 0 {
		return
	}
	for i := 1; i < len(merged); i++ {
		entry := merged[i]
		j := i - 1
		for j >= 0 && merged[j].DurationMS < entry.DurationMS {
			merged[j+1] = merged[j]
			j--
		}
		merged[j+1] = entry
	}
	if len(merged) > maxPersistedSlowTools {
		merged = merged[:maxPersistedSlowTools]
	}
	if raw, err := json.Marshal(merged); err == nil {
		t.SlowTools = string(raw)
	}
}

// maxPersistedSlowTools bounds the rolled-up list so a step with many failover
// attempts cannot grow an unbounded blob in the row.
const maxPersistedSlowTools = 5
