package cursorusage

import (
	"strconv"
	"testing"
	"time"
)

func ts(t time.Time) string { return strconv.FormatInt(t.UnixMilli(), 10) }

func chargedEvent(at time.Time, cents float64) UsageEvent {
	return UsageEvent{
		Timestamp:    ts(at),
		Kind:         "USAGE_EVENT_KIND_USAGE_BASED",
		ChargedCents: cents,
		TokenUsage:   &TokenUsage{InputTokens: 10, OutputTokens: 5},
	}
}

func TestAttributeSingleRun(t *testing.T) {
	base := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	run := RunWindow{ID: 1, Start: base, End: base.Add(10 * time.Minute)}

	events := []UsageEvent{
		chargedEvent(base.Add(time.Minute), 100),      // inside
		chargedEvent(base.Add(5*time.Minute), 50),     // inside
		chargedEvent(base.Add(11*time.Minute), 25),    // inside via skew
		chargedEvent(base.Add(30*time.Minute), 99999), // outside: dropped
	}

	got := Attribute(events, []RunWindow{run}, 2*time.Minute)
	a := got[1]
	if a.Events != 3 || a.Ambiguous != 0 {
		t.Fatalf("attribution = %+v, want 3 events, 0 ambiguous", a)
	}
	if a.CostUSD != 1.75 {
		t.Errorf("CostUSD = %v, want 1.75", a.CostUSD)
	}
	if a.InputTokens != 30 || a.OutputTokens != 15 {
		t.Errorf("tokens = %d/%d, want 30/15", a.InputTokens, a.OutputTokens)
	}
}

// Two fully overlapping runs with distinct token tuples: the fingerprint pass
// must attribute both events correctly even though every event lies inside
// both windows (the case the window-only matcher gave up on).
func TestAttributeFingerprintDisambiguatesOverlap(t *testing.T) {
	base := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	runs := []RunWindow{
		{ID: 1, Start: base, End: base.Add(10 * time.Minute),
			InputTokens: 30641, OutputTokens: 12, CacheWriteTokens: 29135, CacheReadTokens: 1500},
		{ID: 2, Start: base.Add(time.Minute), End: base.Add(9 * time.Minute),
			InputTokens: 105455, OutputTokens: 153, CacheWriteTokens: 0, CacheReadTokens: 75488},
	}
	ev1 := UsageEvent{
		Timestamp:    ts(base.Add(5 * time.Minute)),
		Kind:         "USAGE_EVENT_KIND_USAGE_BASED",
		ChargedCents: 100,
		TokenUsage:   &TokenUsage{InputTokens: 6, OutputTokens: 12, CacheWriteTokens: 29135, CacheReadTokens: 1500},
	}
	ev2 := UsageEvent{
		Timestamp:    ts(base.Add(6 * time.Minute)),
		Kind:         "USAGE_EVENT_KIND_USAGE_BASED",
		ChargedCents: 572,
		TokenUsage:   &TokenUsage{InputTokens: 29967, OutputTokens: 153, CacheWriteTokens: 0, CacheReadTokens: 75488},
	}

	got := Attribute([]UsageEvent{ev1, ev2}, runs, 2*time.Minute)
	if a := got[1]; !a.Fingerprinted || a.Events != 1 || a.CostUSD != 1.00 || a.Ambiguous != 0 {
		t.Errorf("run 1 = %+v, want fingerprinted $1.00", a)
	}
	if a := got[2]; !a.Fingerprinted || a.Events != 1 || a.CostUSD != 5.72 || a.Ambiguous != 0 {
		t.Errorf("run 2 = %+v, want fingerprinted $5.72", a)
	}
}

// Two OVERLAPPING runs with IDENTICAL token tuples (theoretical): the
// fingerprint pass must not guess between them, and the window pass then
// reports the events as ambiguous — nothing is attributed.
func TestAttributeIdenticalTuplesStayAmbiguous(t *testing.T) {
	base := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	runs := []RunWindow{
		{ID: 1, Start: base, End: base.Add(10 * time.Minute), InputTokens: 100, OutputTokens: 10},
		{ID: 2, Start: base.Add(time.Minute), End: base.Add(11 * time.Minute), InputTokens: 100, OutputTokens: 10},
	}
	mk := func(at time.Time) UsageEvent {
		return UsageEvent{Timestamp: ts(at), Kind: "USAGE_EVENT_KIND_USAGE_BASED",
			ChargedCents: 100, TokenUsage: &TokenUsage{InputTokens: 100, OutputTokens: 10}}
	}
	got := Attribute([]UsageEvent{mk(base.Add(2 * time.Minute)), mk(base.Add(3 * time.Minute))}, runs, 0)
	for id, a := range got {
		if a.Events != 0 || a.Ambiguous != 2 {
			t.Errorf("run %d = %+v, want 0 events and 2 ambiguous (no guessing)", id, a)
		}
	}
}

func TestAttributeOverlapIsAmbiguous(t *testing.T) {
	base := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	runs := []RunWindow{
		{ID: 1, Start: base, End: base.Add(10 * time.Minute)},
		{ID: 2, Start: base.Add(5 * time.Minute), End: base.Add(15 * time.Minute)},
	}
	events := []UsageEvent{
		chargedEvent(base.Add(2*time.Minute), 100),  // only run 1
		chargedEvent(base.Add(7*time.Minute), 200),  // overlap: ambiguous
		chargedEvent(base.Add(14*time.Minute), 300), // only run 2
	}

	got := Attribute(events, runs, 0)
	if got[1].Events != 1 || got[1].CostUSD != 1.00 || got[1].Ambiguous != 1 {
		t.Errorf("run 1 = %+v, want 1 event $1.00 with 1 ambiguous", got[1])
	}
	if got[2].Events != 1 || got[2].CostUSD != 3.00 || got[2].Ambiguous != 1 {
		t.Errorf("run 2 = %+v, want 1 event $3.00 with 1 ambiguous", got[2])
	}
}

// cursor-agent CLI events report isHeadless=false just like IDE usage
// (verified live against a real CLI run), so the flag must NOT exclude
// events from attribution.
func TestAttributeKeepsHeadlessFalseEvents(t *testing.T) {
	base := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	run := RunWindow{ID: 1, Start: base, End: base.Add(10 * time.Minute)}

	notHeadless := false
	ev := chargedEvent(base.Add(time.Minute), 100)
	ev.IsHeadless = &notHeadless

	got := Attribute([]UsageEvent{ev}, []RunWindow{run}, 0)
	if got[1].Events != 1 || got[1].CostUSD != 1.00 {
		t.Errorf("attribution = %+v, want isHeadless=false event attributed (CLI runs report false)", got[1])
	}
}

func TestAttributeIgnoresGarbageTimestamps(t *testing.T) {
	base := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	run := RunWindow{ID: 1, Start: base, End: base.Add(10 * time.Minute)}
	ev := chargedEvent(base.Add(time.Minute), 100)
	ev.Timestamp = "not-a-number"

	got := Attribute([]UsageEvent{ev}, []RunWindow{run}, 0)
	if got[1].Events != 0 {
		t.Errorf("attribution = %+v, want garbage timestamp dropped", got[1])
	}
}
