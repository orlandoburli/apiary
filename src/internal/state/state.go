// Package state defines the canonical lifecycle vocabulary shared by every
// layer of Apiary: dispatch jobs, internal tasks, workflow instances, and step
// runs.
//
// Historically each layer declared its own state strings, and no two agreed.
// Three different words meant "accepted but not started" (queued, registered,
// pending), three meant "finished successfully" (done, passed, succeeded), and
// three meant "parked" (approval_waiting, waiting, leased). Every layer boundary
// therefore needed a hand-written translation at the call site, and the
// dashboard papered over the rest by renaming states at render time.
//
// The modelling error underneath was that the *state* axis and the *reason*
// axis were conflated: approval_waiting, waiting and interrupted are all the
// state "blocked" with different reasons, and skipped_cached is "skipped" with
// a reason. Encoding the reason into the state name multiplied the vocabulary
// and still lost information. This package separates them.
//
// # Reading legacy values
//
// Normalize maps any value from any of the historical vocabularies onto the
// canonical set, so a binary can read a database written by an older or newer
// build. It is deliberately total and lossless-on-failure: an unrecognised
// value is returned unchanged rather than silently becoming something else.
package state

// State is the canonical lifecycle state of a job, task, workflow instance, or
// step run. Layers differ only in which subset they can reach:
//
//	dispatch job      queued running done failed canceled
//	task              queued running blocked done failed canceled
//	workflow instance queued running blocked done failed canceled
//	step run          queued running blocked done failed skipped
type State string

const (
	// Queued means accepted, with no execution begun.
	Queued State = "queued"
	// Running means actively executing right now.
	Running State = "running"
	// Blocked means parked awaiting something external. See Reason for what.
	Blocked State = "blocked"
	// Done means finished successfully.
	Done State = "done"
	// Failed means finished unsuccessfully with no attempts remaining. It is
	// terminal: work that will be retried is Blocked with ReasonRetryBackoff,
	// so Failed reliably means "a human needs to look at this".
	Failed State = "failed"
	// Canceled means terminated by an operator.
	Canceled State = "canceled"
	// Skipped means never run by design. See Reason for why.
	Skipped State = "skipped"
)

// Reason explains a non-obvious state. It is stored beside the state (in
// blocked_reason / skipped_reason) rather than encoded into it, which is what
// keeps the state vocabulary small enough to be worth having.
type Reason string

const (
	// ReasonApproval — parked on a human approval gate.
	ReasonApproval Reason = "approval"
	// ReasonCI — parked on a wait_for step watching an external CI run.
	ReasonCI Reason = "ci"
	// ReasonDependency — parked on a wait_for step watching a blocker issue.
	ReasonDependency Reason = "dependency"
	// ReasonRetryBackoff — the last attempt failed and attempts remain. This is
	// what keeps Failed terminal.
	ReasonRetryBackoff Reason = "retry_backoff"
	// ReasonInterrupted — execution stopped abnormally, typically a daemon
	// restart mid-step. Reconciled at the next daemon start.
	ReasonInterrupted Reason = "interrupted"
	// ReasonCached — a step was skipped because a cached result was reused.
	ReasonCached Reason = "cached"
)

// IsTerminal reports whether no further transition is possible without operator
// action. Callers should prefer this over enumerating live states: a negative
// filter cannot miss a state that is added later, whereas a positive one can,
// and silently skipping a live row is how work stops being dispatched.
func (s State) IsTerminal() bool {
	switch s {
	case Done, Failed, Canceled, Skipped:
		return true
	}
	return false
}

// String returns the state's wire value.
func (s State) String() string { return string(s) }

// legacy maps every historical state string onto its canonical state and, where
// the legacy name encoded one, the reason it was hiding.
//
// Note that "leased" maps to Running rather than to a blocked state: a lease is
// granted when a worker claims a job in order to execute it, so a leased job is
// work in progress, not work waiting. Dispatch jobs accordingly never reach
// Blocked.
var legacy = map[string]struct {
	state  State
	reason Reason
}{
	// Already canonical.
	"queued":   {Queued, ""},
	"running":  {Running, ""},
	"blocked":  {Blocked, ""},
	"done":     {Done, ""},
	"failed":   {Failed, ""},
	"canceled": {Canceled, ""},
	"skipped":  {Skipped, ""},

	// Not started.
	"registered": {Queued, ""},
	"pending":    {Queued, ""},

	// In progress.
	"leased": {Running, ""},

	// Parked — the reason was encoded in the name.
	"approval_waiting": {Blocked, ReasonApproval},
	"waiting":          {Blocked, ReasonCI},
	"interrupted":      {Blocked, ReasonInterrupted},

	// Succeeded.
	"passed":    {Done, ""},
	"succeeded": {Done, ""},
	"success":   {Done, ""},

	// Skipped, with the reason encoded in the name.
	"skipped_cached": {Skipped, ReasonCached},
}

// Normalize maps a state string from any historical vocabulary onto the
// canonical set.
//
// An unrecognised value is returned unchanged, cast to State. Normalize never
// invents a state: a value it does not know is surfaced as-is so it shows up in
// the UI and in tests rather than being quietly absorbed into a wrong bucket.
func Normalize(s string) State {
	if m, ok := legacy[s]; ok {
		return m.state
	}
	return State(s)
}

// NormalizeWithReason is Normalize plus the reason the legacy name encoded, for
// the states whose old names carried one (approval_waiting, waiting,
// interrupted, skipped_cached). The reason is empty when the legacy value
// carried none, including for values that are already canonical — a canonical
// row stores its reason in its own column, which this function cannot see.
//
// The "waiting" mapping is lossy: the old state covered both CI waits and
// dependency waits without distinguishing them, so it resolves to ReasonCI, the
// dominant case. The information to do better does not exist in those rows.
func NormalizeWithReason(s string) (State, Reason) {
	if m, ok := legacy[s]; ok {
		return m.state, m.reason
	}
	return State(s), ""
}
