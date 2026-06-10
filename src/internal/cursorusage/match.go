package cursorusage

import "time"

// RunWindow is the wall-clock span of one finished run (one task_executions
// row) awaiting cost attribution.
type RunWindow struct {
	ID    int64
	Start time.Time
	End   time.Time
}

// Attribution is the outcome of matching events to one run.
type Attribution struct {
	// CostUSD is the summed billed cost of all events attributed to the run.
	CostUSD float64
	// Events is how many events were attributed (including $0 not-charged ones).
	Events int
	// InputTokens/OutputTokens sum the attributed events' token counts so the
	// caller can sanity-check against the run's own token totals.
	InputTokens  int
	OutputTokens int
	// Ambiguous counts events that fell inside this run's window but also inside
	// another candidate's. They are attributed to no one, so when Ambiguous > 0
	// CostUSD is a lower bound.
	Ambiguous int
}

// Attribute assigns usage events to runs by timestamp containment. An event
// belongs to a run when its timestamp falls within [Start-skew, End+skew].
// Events that match no run are dropped (other activity on the account); events
// that match more than one run are counted as ambiguous on every candidate and
// attributed to none — wrongly attributing money is worse than undercounting.
// isHeadless is NOT consulted: cursor-agent CLI runs report false just like
// IDE usage (verified live), so the flag cannot separate the two. Interactive
// usage concurrent with a run on the same account is instead caught by the
// caller's token-sum sanity check against the run's own counts.
func Attribute(events []UsageEvent, runs []RunWindow, skew time.Duration) map[int64]*Attribution {
	out := make(map[int64]*Attribution, len(runs))
	for _, r := range runs {
		out[r.ID] = &Attribution{}
	}
	for _, ev := range events {
		t := ev.Time()
		if t.IsZero() {
			continue
		}
		var hits []int64
		for _, r := range runs {
			if !t.Before(r.Start.Add(-skew)) && !t.After(r.End.Add(skew)) {
				hits = append(hits, r.ID)
			}
		}
		switch len(hits) {
		case 0:
			continue
		case 1:
			a := out[hits[0]]
			a.Events++
			a.CostUSD += ev.CostUSD()
			if ev.TokenUsage != nil {
				a.InputTokens += ev.TokenUsage.InputTokens + ev.TokenUsage.CacheWriteTokens + ev.TokenUsage.CacheReadTokens
				a.OutputTokens += ev.TokenUsage.OutputTokens
			}
		default:
			for _, id := range hits {
				out[id].Ambiguous++
			}
		}
	}
	return out
}
