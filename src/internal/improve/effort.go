package improve

import "fmt"

// Effort selects how much history is mined, how much of the workspace is read,
// and how hard each proposal is scrutinised.
//
// Effort scales *how much* is read — never *which kinds* of file may change.
// Even at quick the advisor may propose a soul or skill edit if the metrics
// point there; it just reads less to reach that conclusion.
type Effort string

const (
	EffortQuick    Effort = "quick"
	EffortStandard Effort = "standard"
	EffortDeep     Effort = "deep"
)

// Knobs are the concrete parameters an effort level expands into.
type Knobs struct {
	// DefaultWindow is used when --since was not given.
	DefaultWindow string
	// HotspotLimit caps how many steps get transcripts read.
	HotspotLimit int
	// TranscriptsPerHotspot includes one successful control run, so a value of 1
	// means "the control only" and 3 means "two failures plus a control".
	TranscriptsPerHotspot int
	// TranscriptByteBudget truncates each excerpt (head + tail, middle elided).
	// Agent sessions for a substantial step routinely run to hundreds of
	// kilobytes, so a small budget keeps a percent or two of the file and the
	// excerpt stops being evidence of anything. These are sized to keep enough
	// of both ends to see how a run started and how it ended.
	TranscriptByteBudget int
	// WorkspaceBreadth decides which prose files are inlined into the prompt.
	WorkspaceBreadth Breadth
	// MaxTurns caps the advisor's own agent turns. 0 means the agent's own cap.
	MaxTurns int
	// Critic runs a second pass that argues against each proposal before it is
	// shown. Prose edits cannot be validated mechanically, so at deep effort
	// this is the only automated check standing between a plausible-sounding
	// instruction rewrite and a silent behaviour regression.
	Critic bool
}

// Breadth selects how much of the config workspace reaches the prompt.
type Breadth int

const (
	// BreadthFlagged reads souls and skills only for agents that appear in a
	// finding-worthy metric.
	BreadthFlagged Breadth = iota
	// BreadthActive reads souls and skills for every agent with runs in the window.
	BreadthActive
	// BreadthAll reads the entire config workspace.
	BreadthAll
)

// ParseEffort validates an effort string.
func ParseEffort(s string) (Effort, error) {
	switch Effort(s) {
	case EffortQuick, EffortStandard, EffortDeep:
		return Effort(s), nil
	case "":
		return EffortStandard, nil
	default:
		return "", fmt.Errorf("invalid effort %q: want quick, standard or deep", s)
	}
}

// Expand returns the knobs for an effort level.
func (e Effort) Expand() Knobs {
	switch e {
	case EffortQuick:
		return Knobs{
			DefaultWindow:         "7d",
			HotspotLimit:          0,
			TranscriptsPerHotspot: 0, // aggregates only — no transcripts, one agent call
			WorkspaceBreadth:      BreadthFlagged,
			MaxTurns:              0,
			Critic:                false,
		}
	case EffortDeep:
		return Knobs{
			DefaultWindow:         "90d",
			HotspotLimit:          15,
			TranscriptsPerHotspot: 5,
			TranscriptByteBudget:  40000,
			WorkspaceBreadth:      BreadthAll,
			MaxTurns:              0,
			Critic:                true,
		}
	default: // EffortStandard
		return Knobs{
			DefaultWindow:         "14d",
			HotspotLimit:          5,
			TranscriptsPerHotspot: 2,
			TranscriptByteBudget:  24000,
			WorkspaceBreadth:      BreadthActive,
			MaxTurns:              0,
			Critic:                false,
		}
	}
}
