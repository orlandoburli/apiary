package cursorusage

import "time"

// fingerprintSkew is the generous timestamp gate applied to fingerprint
// matches: the event must land within the run's window widened by this much.
// Exact token equality carries the matching weight; the gate only prevents a
// (theoretical) identical-tuple event from another time being picked up.
const fingerprintSkew = 15 * time.Minute

// RunWindow is one finished run (one task_executions row) awaiting cost
// attribution: its wall-clock span plus its token tuple as reported by the
// CLI's final result event.
//
// Verified live: one cursor-agent invocation produces exactly ONE dashboard
// usage event, whose tokenUsage equals the CLI's reported usage digit for
// digit — so the tuple is a near-unique fingerprint of the run.
type RunWindow struct {
	ID    int64
	Start time.Time
	End   time.Time
	// InputTokens is the full billed input (pure input + cache write + cache
	// read), matching how apiary stores usage. OutputTokens, CacheWriteTokens
	// and CacheReadTokens complete the fingerprint.
	InputTokens      int
	OutputTokens     int
	CacheWriteTokens int
	CacheReadTokens  int
}

// fingerprintMatch reports whether the event's token tuple equals the run's.
func fingerprintMatch(ev UsageEvent, r RunWindow) bool {
	tu := ev.TokenUsage
	if tu == nil {
		return false
	}
	return tu.InputTokens+tu.CacheWriteTokens+tu.CacheReadTokens == r.InputTokens &&
		tu.OutputTokens == r.OutputTokens &&
		tu.CacheWriteTokens == r.CacheWriteTokens &&
		tu.CacheReadTokens == r.CacheReadTokens
}

func inWindow(t time.Time, r RunWindow, skew time.Duration) bool {
	return !t.Before(r.Start.Add(-skew)) && !t.After(r.End.Add(skew))
}

// Attribution is the outcome of matching events to one run.
type Attribution struct {
	// CostUSD is the summed billed cost of all events attributed to the run.
	CostUSD float64
	// Events is how many events were attributed (normally exactly 1: one CLI
	// invocation produces one usage event).
	Events int
	// Fingerprinted is true when the attribution came from an exact token-tuple
	// match (self-validating). False means the weaker window fallback was used
	// and the caller should sanity-check the totals.
	Fingerprinted bool
	// InputTokens/OutputTokens sum the attributed events' token counts so the
	// caller can sanity-check fallback attributions against the run's counts.
	InputTokens  int
	OutputTokens int
	// Ambiguous counts events that could not be assigned because they fit more
	// than one candidate run. When Ambiguous > 0, CostUSD is a lower bound.
	Ambiguous int
}

// Attribute assigns usage events to runs in two passes.
//
// Pass 1 — fingerprint: an event whose token tuple exactly equals a run's
// recorded usage (and lands near its window) belongs to that run. Token
// tuples are high-entropy, so this disambiguates even fully overlapping
// concurrent runs. Runs whose CLI died before the final result event carry no
// usage and are excluded upstream (total_tokens > 0).
//
// Pass 2 — window fallback for runs that pass 1 left empty, considering only
// events pass 1 did not consume: an event inside exactly one such run's
// window (±skew) is attributed; an event inside several is counted as
// ambiguous on each and attributed to none — wrongly attributing money is
// worse than undercounting. isHeadless is never consulted: cursor-agent CLI
// runs report false, identical to interactive IDE usage (verified live).
func Attribute(events []UsageEvent, runs []RunWindow, skew time.Duration) map[int64]*Attribution {
	out := make(map[int64]*Attribution, len(runs))
	for _, r := range runs {
		out[r.ID] = &Attribution{}
	}

	addEvent := func(a *Attribution, ev UsageEvent) {
		a.Events++
		a.CostUSD += ev.CostUSD()
		if ev.TokenUsage != nil {
			a.InputTokens += ev.TokenUsage.InputTokens + ev.TokenUsage.CacheWriteTokens + ev.TokenUsage.CacheReadTokens
			a.OutputTokens += ev.TokenUsage.OutputTokens
		}
	}

	// Pass 1: fingerprint matches.
	taken := make([]bool, len(events))
	matched := make(map[int64]bool, len(runs))
	for i, ev := range events {
		t := ev.Time()
		if t.IsZero() {
			continue
		}
		var hits []RunWindow
		for _, r := range runs {
			if !matched[r.ID] && fingerprintMatch(ev, r) && inWindow(t, r, fingerprintSkew) {
				hits = append(hits, r)
			}
		}
		if len(hits) == 1 {
			a := out[hits[0].ID]
			addEvent(a, ev)
			a.Fingerprinted = true
			matched[hits[0].ID] = true
			taken[i] = true
		}
		// >1 identical tuples in flight at once: leave for the window pass.
	}

	// Pass 2: window containment for whatever is left.
	for i, ev := range events {
		if taken[i] {
			continue
		}
		t := ev.Time()
		if t.IsZero() {
			continue
		}
		var hits []int64
		for _, r := range runs {
			if !matched[r.ID] && inWindow(t, r, skew) {
				hits = append(hits, r.ID)
			}
		}
		switch len(hits) {
		case 0:
			// Someone else's activity (IDE usage, other accounts' runs): drop.
		case 1:
			addEvent(out[hits[0]], ev)
		default:
			for _, id := range hits {
				out[id].Ambiguous++
			}
		}
	}
	return out
}
