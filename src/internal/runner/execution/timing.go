package execution

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
)

// Wall-clock attribution for a CLI run (issue #399).
//
// step_runs already records tokens, cost, turns and tool-call counts — everything
// except where the wall clock actually went. A step that takes 80 minutes gives no
// hint whether the model was thinking, writing, or blocked on a subprocess it
// launched, and the three have completely different fixes (effort/model, prompt
// size, and fixing the thing being waited on respectively). This tracker folds the
// provider's stream-json events into that breakdown as they are scanned, using
// data the runner already parses and throws away.
//
// # How the attribution works
//
// The CLIs emit COMPLETE-message events (`assistant`, `user`, `result`), not token
// deltas, so there is no direct "model started writing" signal. What we have is the
// arrival time of each event, and the tracker attributes the gap between two
// consecutive events to whatever was outstanding when the gap opened:
//
//   - Any foreground tool call open (an `assistant` message emitted `tool_use`
//     blocks whose `tool_result`s have not all come back) → tool wait.
//   - Otherwise the model is the only thing that can be busy → model latency,
//     split into thinking vs writing by the `system:thinking_tokens` events the
//     CLI emits while a thinking block is streaming (see splitting note below).
//   - The head and tail of the run — process spawn before the first event, and
//     teardown after the last — land in Other, so the buckets sum to roughly the
//     run's wall clock rather than to "time we happened to understand".
//
// # Overlap is real, so buckets are a timeline, not a sum of durations
//
// One `assistant` message can carry several `tool_use` blocks that run
// concurrently, and a background task keeps running while the model writes. Summing
// per-tool durations would therefore exceed the run's wall clock and produce
// percentages over 100%. The buckets above are exclusive — each instant of wall
// clock is counted once, in exactly one bucket — while per-call durations are
// reported separately in SlowTools, where overlap is expected and fine.
//
// BackgroundMS is deliberately the one non-exclusive figure: it is the union of
// the intervals where at least one background task was live, and it overlaps the
// exclusive buckets by design (the model may well be writing while a test suite
// runs). It answers "how much of this step had background work outstanding",
// which is not the same question as "what was the step waiting on".
//
// # Thinking vs writing is a heuristic
//
// `system:thinking_tokens` is documented by the CLI as a live estimate emitted
// during the redacted-thinking phase, so it is present for some thinking and absent
// for other thinking, and entirely absent on providers that do not emit it (the
// Cursor CLI). Rather than silently splitting a gap 0/100 when the signal is
// missing, unsplittable model latency is reported as ModelMS and the caller
// displays it as un-attributed. Thinking/Writing are only ever populated from a
// real signal.
type timingTracker struct {
	mu  sync.Mutex
	now func() time.Time

	// last is the timestamp of the most recently observed event; the gap between
	// it and the next event is what gets attributed.
	last time.Time
	// thinkingSeen records that a thinking frame has arrived during the current
	// turn. It stays set until the turn ends, because the events between the last
	// thinking frame and the model's message are not all thinking frames —
	// post_turn_summary, hook bookends and status frames all interleave. Treating
	// this as a one-shot flag consumed by the next event attributed a 20-second
	// write as un-attributed latency, because one unrelated system frame landed in
	// the middle of it.
	thinkingSeen bool

	thinking, writing, model, toolWait, other time.Duration

	openTools map[string]*openCall
	openTasks map[string]*openCall
	// byToolUse indexes finished entries by tool_use id, so the two reports of one
	// backgrounded call collapse into a single entry.
	byToolUse map[string]int
	// bg holds the closed intervals during which at least one background task was
	// live, in arrival order; overlapping entries are merged when reported.
	bg []interval
	// finished records completed calls (foreground tools and background tasks)
	// for the slowest-N report.
	finished []model.ToolTiming
}

type openCall struct {
	name    string
	label   string
	started time.Time
	// background distinguishes a task_started bookend from a foreground tool call,
	// so the slowest-N list can say which of the two a long entry was.
	background bool
	// toolUses is the tool_use id this call belongs to. A background task the agent
	// launched through a tool (the Task tool, a backgrounded Bash) is reported
	// BOTH as a foreground tool call and as a background bookend carrying the same
	// id; it correlates the two so one piece of work produces one entry.
	toolUses string
}

type interval struct{ start, end time.Time }

// maxSlowTools caps how many of the slowest calls are kept. Five was enough to
// explain two thirds of the 82-minute step in issue #399; the point is to name the
// handful of calls worth acting on, not to mirror the whole transcript.
const maxSlowTools = 5

// slowToolMinDuration filters out the noise floor. Sub-second calls can never be
// the reason a step took 80 minutes, and letting them into the list on a fast step
// would push out the entries that matter.
const slowToolMinDuration = time.Second

// newTimingTracker starts a tracker whose timeline opens at start — the moment the
// runner spawned the provider process, so the gap before the first stream event
// (process spawn, prompt upload, connection setup) is accounted for rather than
// silently dropped.
func newTimingTracker(start time.Time, now func() time.Time) *timingTracker {
	if now == nil {
		now = time.Now
	}
	return &timingTracker{
		now:       now,
		last:      start,
		openTools: map[string]*openCall{},
		openTasks: map[string]*openCall{},
		byToolUse: map[string]int{},
	}
}

// timingEvent decodes the stream-json fields the tracker needs. It is deliberately
// separate from streamEvent and transcriptEvent: those decode content for usage
// totals and for rendering, while this one needs the tool_use/tool_result
// correlation ids and the background-task bookend payloads that neither of them
// looks at.
type timingEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message struct {
		Content []struct {
			Type      string          `json:"type"`
			Name      string          `json:"name"`
			ID        string          `json:"id"`
			Input     json.RawMessage `json:"input"`
			ToolUseID string          `json:"tool_use_id"`
		} `json:"content"`
	} `json:"message"`

	// Background-task bookends. The CLI emits system:task_started when the agent
	// launches background work (a Task-tool subagent, a workflow) and
	// system:task_notification when it settles, correlated by task_id.
	TaskID string `json:"task_id"`
	// ToolUseID correlates a bookend with the tool call that launched the task, so
	// the two reports of one wait can be collapsed into one entry.
	ToolUseID    string `json:"tool_use_id"`
	Description  string `json:"description"`
	TaskType     string `json:"task_type"`
	SubagentType string `json:"subagent_type"`
	WorkflowName string `json:"workflow_name"`
	Status       string `json:"status"`
	// Usage rides on task_notification and carries the task's own measured
	// duration, which is more accurate than our arrival-time delta.
	Usage *struct {
		DurationMs int64 `json:"duration_ms"`
	} `json:"usage"`
}

// Feed consumes one raw provider stdout line, attributing the wall clock since the
// previous event. Safe for concurrent use; the CliRunner calls it from the stdout
// scanning goroutine.
func (t *timingTracker) Feed(line string) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "{") {
		return
	}
	var ev timingEvent
	if err := json.Unmarshal([]byte(trimmed), &ev); err != nil || ev.Type == "" {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	at := t.now()
	// A turn's thinking is delivered as its own `assistant` message, separate from
	// and earlier than the message carrying the visible output — so a thinking-only
	// message closes a stretch of thinking, exactly like a thinking_tokens frame,
	// and must not be mistaken for the model having produced its answer.
	thinkingOnly, hasOutput := assistantShape(ev)
	t.attribute(at, (ev.Type == "system" && ev.Subtype == "thinking_tokens") || thinkingOnly)

	switch ev.Type {
	case "assistant":
		// Only a message carrying real output ends the turn. Ending it on the
		// thinking message instead left the whole write — 2.3 seconds of a
		// 10-second run — attributed to nothing.
		if hasOutput {
			t.endTurn()
		}
		for _, c := range ev.Message.Content {
			if c.Type != "tool_use" || c.ID == "" {
				continue
			}
			t.openTools[c.ID] = &openCall{
				name:     c.Name,
				label:    toolLabel(truncateInput(c.Input)),
				started:  at,
				toolUses: c.ID,
			}
		}
	case "user":
		// Tool results feed the model a new turn.
		t.endTurn()
		for _, c := range ev.Message.Content {
			if c.Type != "tool_result" || c.ToolUseID == "" {
				continue
			}
			if call, ok := t.openTools[c.ToolUseID]; ok {
				delete(t.openTools, c.ToolUseID)
				t.record(call, at.Sub(call.started))
			}
		}
	case "system":
		switch ev.Subtype {
		case "task_started":
			if ev.TaskID == "" {
				break
			}
			t.openTasks[ev.TaskID] = &openCall{
				name:       backgroundName(ev),
				label:      toolLabel(ev.Description),
				started:    at,
				background: true,
				toolUses:   ev.ToolUseID,
			}
		case "task_notification":
			call, ok := t.openTasks[ev.TaskID]
			if !ok {
				break
			}
			delete(t.openTasks, ev.TaskID)
			t.bg = append(t.bg, interval{start: call.started, end: at})
			// Prefer the task's self-reported duration: our bookends are bounded by
			// when the CLI got round to emitting them, the task's own measurement is
			// not.
			d := at.Sub(call.started)
			if ev.Usage != nil && ev.Usage.DurationMs > 0 {
				d = time.Duration(ev.Usage.DurationMs) * time.Millisecond
			}
			if ev.Status != "" && ev.Status != "completed" {
				call.label = strings.TrimSpace(call.label + " (" + ev.Status + ")")
			}
			t.record(call, d)
		}
	}
}

// attribute charges the wall clock since the previous event to the right bucket.
// endsThinking marks that the event closing this gap is a thinking_tokens frame,
// which means the model spent the gap producing thinking tokens.
func (t *timingTracker) attribute(at time.Time, endsThinking bool) {
	gap := at.Sub(t.last)
	t.last = at
	if endsThinking {
		t.thinkingSeen = true
	}
	if gap <= 0 {
		return
	}
	switch {
	case len(t.openTools) > 0:
		// A tool call is outstanding, so nothing else can be the reason we waited.
		t.toolWait += gap
	case endsThinking:
		// The gap ended with the model reporting thinking-token progress, so it was
		// spent thinking.
		t.thinking += gap
	case t.thinkingSeen:
		// Thinking streamed earlier in this turn and has stopped, so the model is
		// now producing its visible output. This holds for the whole rest of the
		// turn, not just until the next frame of any kind arrives.
		t.writing += gap
	default:
		// No thinking signal anywhere in this turn — model latency we cannot split.
		t.model += gap
	}
}

// endTurn closes the current model turn, so thinking observed in it does not leak
// into the attribution of the next one.
func (t *timingTracker) endTurn() { t.thinkingSeen = false }

// assistantShape classifies an `assistant` message by what it delivers.
// thinkingOnly marks a message that carries nothing but thinking blocks — the
// model reporting its reasoning, not its answer. hasOutput marks one carrying
// visible text or a tool call, which is what actually ends a turn. Both are false
// for every other event type.
func assistantShape(ev timingEvent) (thinkingOnly, hasOutput bool) {
	if ev.Type != "assistant" || len(ev.Message.Content) == 0 {
		return false, false
	}
	sawThinking := false
	for _, c := range ev.Message.Content {
		switch c.Type {
		case "thinking", "redacted_thinking":
			sawThinking = true
		default:
			hasOutput = true
		}
	}
	return sawThinking && !hasOutput, hasOutput
}

// record adds a completed call to the slowest-N candidates, merging it with the
// other half of the same work when one piece of work was reported twice.
//
// Launching a subagent produces a foreground `Agent` tool call AND a background
// task bookend for the same wait, correlated by tool_use id. Listing both spends
// two of five slots on one thing and reads as two separate problems, so they
// collapse into a single entry: the background half names it far better
// ("agent:general-purpose · explore the auth code" against a blob of tool-call
// JSON), while the foreground half spans the full wait including dispatch, so the
// merged entry takes the better name and the longer duration.
func (t *timingTracker) record(call *openCall, d time.Duration) {
	if d < slowToolMinDuration {
		return
	}
	if call.toolUses != "" {
		if i, ok := t.byToolUse[call.toolUses]; ok {
			existing := &t.finished[i]
			if d.Milliseconds() > existing.DurationMS {
				existing.DurationMS = d.Milliseconds()
			}
			if call.background {
				existing.Name = call.name
				existing.Label = call.label
				existing.Background = true
			}
			return
		}
		t.byToolUse[call.toolUses] = len(t.finished)
	}
	t.finished = append(t.finished, model.ToolTiming{
		Name:       call.name,
		Label:      call.label,
		DurationMS: d.Milliseconds(),
		Background: call.background,
	})
}

// Finish closes the timeline at end (the moment the provider process exited),
// charging the tail to Other and closing any call still outstanding — a run that
// was killed mid-tool should still report what it was stuck on. It returns nil when
// the stream carried nothing attributable, so runners that emit no usable events
// record no timing rather than a misleading all-Other row.
func (t *timingTracker) Finish(end time.Time) *model.Timing {
	t.mu.Lock()
	defer t.mu.Unlock()

	if gap := end.Sub(t.last); gap > 0 {
		// Whatever is still open was open when the process exited; the tail is
		// teardown, not a wait on a tool that will never answer.
		t.other += gap
		t.last = end
	}
	for id, call := range t.openTools {
		delete(t.openTools, id)
		t.record(call, end.Sub(call.started))
	}
	for id, call := range t.openTasks {
		delete(t.openTasks, id)
		t.bg = append(t.bg, interval{start: call.started, end: end})
		t.record(call, end.Sub(call.started))
	}

	out := model.Timing{
		ThinkingMS:   t.thinking.Milliseconds(),
		WritingMS:    t.writing.Milliseconds(),
		ModelMS:      t.model.Milliseconds(),
		ToolWaitMS:   t.toolWait.Milliseconds(),
		OtherMS:      t.other.Milliseconds(),
		BackgroundMS: mergedDuration(t.bg).Milliseconds(),
		SlowTools:    t.slowest(),
	}
	out.TotalMS = out.ThinkingMS + out.WritingMS + out.ModelMS + out.ToolWaitMS + out.OtherMS
	if out.TotalMS == 0 && len(out.SlowTools) == 0 {
		return nil
	}
	return &out
}

func (t *timingTracker) slowest() []model.ToolTiming {
	if len(t.finished) == 0 {
		return nil
	}
	out := make([]model.ToolTiming, len(t.finished))
	copy(out, t.finished)
	sort.SliceStable(out, func(i, j int) bool { return out[i].DurationMS > out[j].DurationMS })
	if len(out) > maxSlowTools {
		out = out[:maxSlowTools]
	}
	return out
}

// mergedDuration returns the total wall clock covered by the union of the
// intervals. Background tasks routinely overlap each other, so adding their
// durations would double-count time during which two suites ran side by side.
func mergedDuration(in []interval) time.Duration {
	if len(in) == 0 {
		return 0
	}
	ordered := make([]interval, len(in))
	copy(ordered, in)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].start.Before(ordered[j].start) })
	var total time.Duration
	cur := ordered[0]
	for _, iv := range ordered[1:] {
		if iv.start.After(cur.end) {
			total += cur.end.Sub(cur.start)
			cur = iv
			continue
		}
		if iv.end.After(cur.end) {
			cur.end = iv.end
		}
	}
	total += cur.end.Sub(cur.start)
	return total
}

// backgroundName labels a background task by what it actually is, preferring the
// most specific identifier the payload carries: the workflow's name, then the
// subagent type, then the generic task type.
func backgroundName(ev timingEvent) string {
	switch {
	case ev.WorkflowName != "":
		return "workflow:" + ev.WorkflowName
	case ev.SubagentType != "":
		return "agent:" + ev.SubagentType
	case ev.TaskType != "":
		return "task:" + ev.TaskType
	}
	return "task"
}

// toolLabelLimit keeps labels short enough to sit in a terminal table cell. The
// full tool input is already in the task log and the transcript; this is a
// recognisable handle, not a record.
const toolLabelLimit = 120

// toolLabel trims a tool input or task description down to a display handle,
// stripping bearer tokens on the way through — labels are persisted and rendered,
// and agents routinely export credentials in Bash calls.
func toolLabel(s string) string {
	s = strings.TrimSpace(jwtPattern.ReplaceAllString(s, "«redacted-jwt»"))
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > toolLabelLimit {
		return s[:toolLabelLimit] + "…"
	}
	return s
}
