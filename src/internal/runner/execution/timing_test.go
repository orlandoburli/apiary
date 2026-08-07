package execution

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeClock advances by a fixed step each time it is read, so a test can express
// "this event arrived N seconds after the last one" by feeding lines through a
// clock it drives explicitly.
type fakeClock struct {
	at time.Time
}

func (c *fakeClock) now() time.Time { return c.at }

func (c *fakeClock) advance(d time.Duration) { c.at = c.at.Add(d) }

// feed drives lines through the tracker, advancing the clock by the paired gap
// BEFORE each line — i.e. gaps[i] is how long the run waited for lines[i].
func feed(t *testing.T, tr *timingTracker, c *fakeClock, gaps []time.Duration, lines []string) {
	t.Helper()
	if len(gaps) != len(lines) {
		t.Fatalf("test bug: %d gaps for %d lines", len(gaps), len(lines))
	}
	for i, line := range lines {
		c.advance(gaps[i])
		tr.Feed(line)
	}
}

func newTestTracker() (*timingTracker, *fakeClock) {
	c := &fakeClock{at: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)}
	return newTimingTracker(c.at, c.now), c
}

func toolUse(id, name, input string) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":%q,"name":%q,"input":%s}]}}`, id, name, input)
}

func toolResult(id string) string {
	return fmt.Sprintf(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":%q,"content":"ok"}]}}`, id)
}

const (
	assistantText  = `{"type":"assistant","message":{"content":[{"type":"text","text":"working on it"}]}}`
	thinkingTokens = `{"type":"system","subtype":"thinking_tokens","estimated_tokens":120,"estimated_tokens_delta":40}`
)

func TestTimingAttributesToolWaitToTheOpenCall(t *testing.T) {
	tr, c := newTestTracker()
	feed(t, tr, c,
		[]time.Duration{2 * time.Second, 30 * time.Second},
		[]string{toolUse("t1", "Bash", `{"command":"go test ./..."}`), toolResult("t1")},
	)
	got := tr.Finish(c.at)

	if got == nil {
		t.Fatal("expected timing")
	}
	if got.ToolWaitMS != 30_000 {
		t.Errorf("ToolWaitMS = %d, want 30000", got.ToolWaitMS)
	}
	// The 2s before the tool call is the model deciding to make it, and there was no
	// thinking signal, so it is un-attributed model latency rather than writing.
	if got.ModelMS != 2_000 {
		t.Errorf("ModelMS = %d, want 2000", got.ModelMS)
	}
	if len(got.SlowTools) != 1 {
		t.Fatalf("SlowTools = %+v, want 1 entry", got.SlowTools)
	}
	if got.SlowTools[0].Name != "Bash" || got.SlowTools[0].DurationMS != 30_000 {
		t.Errorf("slow tool = %+v", got.SlowTools[0])
	}
	if got.SlowTools[0].Background {
		t.Error("foreground tool call marked as background")
	}
}

// Parallel tool calls overlap, so their individual durations must each be recorded
// in full while the exclusive wall-clock bucket counts the overlap only once.
// Getting this wrong is how a breakdown ends up summing past 100%.
func TestTimingCountsOverlappingToolCallsOnceInTheBucket(t *testing.T) {
	tr, c := newTestTracker()
	both := `{"type":"assistant","message":{"content":[` +
		`{"type":"tool_use","id":"a","name":"Bash","input":{"command":"suite-a"}},` +
		`{"type":"tool_use","id":"b","name":"Bash","input":{"command":"suite-b"}}]}}`
	feed(t, tr, c,
		[]time.Duration{time.Second, 60 * time.Second, 30 * time.Second},
		[]string{both, toolResult("a"), toolResult("b")},
	)
	got := tr.Finish(c.at)

	if got.ToolWaitMS != 90_000 {
		t.Errorf("ToolWaitMS = %d, want 90000 (wall clock, counted once)", got.ToolWaitMS)
	}
	if len(got.SlowTools) != 2 {
		t.Fatalf("SlowTools = %+v, want 2", got.SlowTools)
	}
	// 60s + 90s of individual durations against 90s of wall clock: overlap is
	// expected in the per-call list and must not be normalised away.
	if got.SlowTools[0].DurationMS != 90_000 || got.SlowTools[1].DurationMS != 60_000 {
		t.Errorf("per-call durations = %d, %d; want 90000, 60000 slowest-first",
			got.SlowTools[0].DurationMS, got.SlowTools[1].DurationMS)
	}
	if got.TotalMS != got.ThinkingMS+got.WritingMS+got.ModelMS+got.ToolWaitMS+got.OtherMS {
		t.Errorf("TotalMS %d is not the sum of the exclusive buckets", got.TotalMS)
	}
}

func TestTimingSplitsThinkingFromWriting(t *testing.T) {
	tr, c := newTestTracker()
	feed(t, tr, c,
		[]time.Duration{10 * time.Second, 5 * time.Second, 20 * time.Second},
		[]string{thinkingTokens, thinkingTokens, assistantText},
	)
	got := tr.Finish(c.at)

	// Both gaps that END on a thinking frame are thinking; the gap from the last
	// thinking frame to the visible message is writing.
	if got.ThinkingMS != 15_000 {
		t.Errorf("ThinkingMS = %d, want 15000", got.ThinkingMS)
	}
	if got.WritingMS != 20_000 {
		t.Errorf("WritingMS = %d, want 20000", got.WritingMS)
	}
	if got.ModelMS != 0 {
		t.Errorf("ModelMS = %d, want 0 when the thinking signal is present", got.ModelMS)
	}
}

// Providers that never emit thinking_tokens (the Cursor CLI) must report their
// model latency as un-attributed rather than have it silently land in one bucket.
func TestTimingLeavesModelLatencyUnsplitWithoutTheThinkingSignal(t *testing.T) {
	tr, c := newTestTracker()
	feed(t, tr, c, []time.Duration{25 * time.Second}, []string{assistantText})
	got := tr.Finish(c.at)

	if got.ModelMS != 25_000 {
		t.Errorf("ModelMS = %d, want 25000", got.ModelMS)
	}
	if got.ThinkingMS != 0 || got.WritingMS != 0 {
		t.Errorf("thinking/writing = %d/%d, want 0/0 with no signal to split on",
			got.ThinkingMS, got.WritingMS)
	}
}

func TestTimingPairsBackgroundTaskBookends(t *testing.T) {
	tr, c := newTestTracker()
	started := `{"type":"system","subtype":"task_started","task_id":"bg1","description":"run the full suite","task_type":"local_workflow","workflow_name":"verify"}`
	// The task reports 9m50s of its own; our bookends bracket 10m of arrival time.
	// The self-reported figure is the accurate one and must win.
	notified := `{"type":"system","subtype":"task_notification","task_id":"bg1","status":"completed","output_file":"/tmp/x","summary":"done","usage":{"total_tokens":10,"tool_uses":2,"duration_ms":590000}}`
	feed(t, tr, c,
		[]time.Duration{time.Second, 10 * time.Minute},
		[]string{started, notified},
	)
	got := tr.Finish(c.at)

	if got.BackgroundMS != 600_000 {
		t.Errorf("BackgroundMS = %d, want 600000 (bookend to bookend)", got.BackgroundMS)
	}
	if len(got.SlowTools) != 1 {
		t.Fatalf("SlowTools = %+v, want the background task", got.SlowTools)
	}
	entry := got.SlowTools[0]
	if entry.DurationMS != 590_000 {
		t.Errorf("DurationMS = %d, want the task's self-reported 590000", entry.DurationMS)
	}
	if !entry.Background {
		t.Error("background task not marked as background")
	}
	if entry.Name != "workflow:verify" {
		t.Errorf("Name = %q, want workflow:verify", entry.Name)
	}
	if entry.Label != "run the full suite" {
		t.Errorf("Label = %q", entry.Label)
	}
}

// BackgroundMS is a union, not a sum: two suites running side by side for ten
// minutes is ten minutes of background work, not twenty.
func TestTimingMergesOverlappingBackgroundTasks(t *testing.T) {
	tr, c := newTestTracker()
	start := func(id string) string {
		return fmt.Sprintf(`{"type":"system","subtype":"task_started","task_id":%q,"description":"suite","task_type":"local_workflow"}`, id)
	}
	done := func(id string) string {
		return fmt.Sprintf(`{"type":"system","subtype":"task_notification","task_id":%q,"status":"completed","output_file":"","summary":""}`, id)
	}
	feed(t, tr, c,
		[]time.Duration{0, time.Minute, 4 * time.Minute, 5 * time.Minute},
		[]string{start("a"), start("b"), done("a"), done("b")},
	)
	got := tr.Finish(c.at)

	if got.BackgroundMS != 10*60*1000 {
		t.Errorf("BackgroundMS = %d, want 600000 (union of a:0-5m and b:1m-10m)", got.BackgroundMS)
	}
}

// A run killed mid-tool is exactly when you most want to know what it was stuck
// on, so an unclosed call must still be reported rather than dropped.
func TestTimingClosesOutstandingCallsOnFinish(t *testing.T) {
	tr, c := newTestTracker()
	feed(t, tr, c, []time.Duration{time.Second}, []string{toolUse("t1", "Bash", `{"command":"sleep 9999"}`)})
	c.advance(5 * time.Minute)
	got := tr.Finish(c.at)

	if len(got.SlowTools) != 1 || got.SlowTools[0].DurationMS != 300_000 {
		t.Fatalf("SlowTools = %+v, want the unclosed Bash call at 300000ms", got.SlowTools)
	}
	// The tail after the last event is teardown, not a wait on a tool result that
	// is never coming.
	if got.OtherMS != 300_000 {
		t.Errorf("OtherMS = %d, want 300000", got.OtherMS)
	}
	if got.ToolWaitMS != 0 {
		t.Errorf("ToolWaitMS = %d, want 0", got.ToolWaitMS)
	}
}

func TestTimingKeepsOnlyTheSlowestCalls(t *testing.T) {
	tr, c := newTestTracker()
	for i := 0; i < maxSlowTools+3; i++ {
		id := fmt.Sprintf("t%d", i)
		c.advance(time.Second)
		tr.Feed(toolUse(id, "Bash", `{"command":"x"}`))
		c.advance(time.Duration(i+1) * time.Second)
		tr.Feed(toolResult(id))
	}
	got := tr.Finish(c.at)

	if len(got.SlowTools) != maxSlowTools {
		t.Fatalf("kept %d slow tools, want %d", len(got.SlowTools), maxSlowTools)
	}
	for i := 1; i < len(got.SlowTools); i++ {
		if got.SlowTools[i-1].DurationMS < got.SlowTools[i].DurationMS {
			t.Fatalf("SlowTools not ordered slowest-first: %+v", got.SlowTools)
		}
	}
}

// Sub-second calls can never explain a long step, and letting them in would push
// out the entries that can.
func TestTimingIgnoresTrivialCalls(t *testing.T) {
	tr, c := newTestTracker()
	feed(t, tr, c,
		[]time.Duration{0, 10 * time.Millisecond},
		[]string{toolUse("t1", "Read", `{"file_path":"/x"}`), toolResult("t1")},
	)
	if got := tr.Finish(c.at); len(got.SlowTools) != 0 {
		t.Errorf("SlowTools = %+v, want none", got.SlowTools)
	}
}

// Labels are persisted and rendered, and agents routinely export tokens in Bash
// calls, so a credential in a tool input must not survive into one.
func TestTimingRedactsCredentialsInLabels(t *testing.T) {
	tr, c := newTestTracker()
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dBjftJeZ4CVPmB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	feed(t, tr, c,
		[]time.Duration{0, 5 * time.Second},
		[]string{toolUse("t1", "Bash", fmt.Sprintf(`{"command":"curl -H 'Authorization: Bearer %s' x"}`, jwt)), toolResult("t1")},
	)
	got := tr.Finish(c.at)

	if len(got.SlowTools) != 1 {
		t.Fatalf("SlowTools = %+v", got.SlowTools)
	}
	if label := got.SlowTools[0].Label; strings.Contains(label, "eyJ") {
		t.Errorf("label leaked a credential: %q", label)
	}
}

// A provider whose stream we understand nothing of should record no timing at all,
// rather than a row that is 100% "other" and reads as a real measurement.
func TestTimingReturnsNilWithoutUsableEvents(t *testing.T) {
	tr, c := newTestTracker()
	feed(t, tr, c,
		[]time.Duration{0, 0},
		[]string{"not json at all", `{"no_type":"here"}`},
	)
	if got := tr.Finish(c.at); got != nil {
		t.Errorf("Finish = %+v, want nil", got)
	}
}

func TestSystemDetailNamesBackgroundTasks(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{
			name: "task_started names the workflow and its description",
			line: `{"type":"system","subtype":"task_started","task_id":"bg1","description":"run the full test suite","task_type":"local_workflow","workflow_name":"verify"}`,
			want: "workflow:verify · run the full test suite · task=bg1",
		},
		{
			name: "subagent task falls back to the agent type",
			line: `{"type":"system","subtype":"task_started","task_id":"bg2","description":"explore the auth code","subagent_type":"Explore"}`,
			want: "agent:Explore · explore the auth code · task=bg2",
		},
		{
			name: "task_notification carries the outcome and measured duration",
			line: `{"type":"system","subtype":"task_notification","task_id":"bg1","status":"failed","output_file":"/tmp/o","summary":"boom","usage":{"total_tokens":1,"tool_uses":1,"duration_ms":90000}}`,
			want: "failed · duration=1m30s · task=bg1",
		},
		{
			name: "unrelated system events stay as they were",
			line: `{"type":"system","subtype":"init","model":"claude-opus-5"}`,
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := systemDetail(tc.line); got != tc.want {
				t.Errorf("systemDetail = %q, want %q", got, tc.want)
			}
		})
	}
}

// The whole point of issue #399's third ask: the log line for a background task
// must name what the task actually is.
func TestFormatStreamLineIncludesBackgroundTaskPayload(t *testing.T) {
	line := `{"type":"system","subtype":"task_started","task_id":"bg1","description":"run the full test suite","task_type":"local_workflow","workflow_name":"verify"}`
	got, ok := formatStreamLine(line)
	if !ok {
		t.Fatal("formatStreamLine did not handle a system event")
	}
	want := "[system:task_started] workflow:verify · run the full test suite · task=bg1"
	if got != want {
		t.Errorf("formatStreamLine = %q, want %q", got, want)
	}
}
