package dashboard

import (
	"strings"
	"testing"
)

// While a warm-up batch is in flight, the same message is not re-dispatched;
// a batch rendered for a stale width is dropped on merge but clears pending so
// the next refresh can re-warm at the new width.
func TestWarmMarkdownDedupAndWidthRace(t *testing.T) {
	a := newTestApp(90, 24)
	md := "## Status\n\n- first\n- second\n"

	cmd := a.warmMarkdownCmd([]LogEntry{{Message: md}})
	if cmd == nil {
		t.Fatal("first warm-up should have work")
	}
	if a.warmMarkdownCmd([]LogEntry{{Message: md}}) != nil {
		t.Error("in-flight message must not be re-dispatched")
	}

	wm, ok := cmd().(mdWarmedMsg)
	if !ok {
		t.Fatalf("warm command returned %T, want mdWarmedMsg", wm)
	}

	// Terminal resized before the batch landed: the cache re-keys to a new width.
	a.model.width = 140
	a.ensureLogCache(a.logMsgWidth())

	a.Update(wm)
	if _, ok := a.logMDCache[md]; ok {
		t.Error("stale-width batch must not merge into the cache")
	}
	if a.logMDPending[md] {
		t.Error("pending must clear even when the batch is dropped")
	}
	if a.warmMarkdownCmd([]LogEntry{{Message: md}}) == nil {
		t.Error("message should be warmable again at the new width")
	}
}

// Messages above maxGlamourBytes never go to glamour: they paint plain-wrapped,
// are cached eagerly, and the warm-up skips them.
func TestOversizedMarkdownStaysPlain(t *testing.T) {
	a := newTestApp(90, 24)
	big := "## Big\n\n" + strings.Repeat("- a very long bullet line of agent output\n", 400)
	if len(big) <= maxGlamourBytes {
		t.Fatalf("test message too small: %d bytes", len(big))
	}
	if glamourEligible(big) {
		t.Fatal("oversized markdown must not be glamour-eligible")
	}

	w := a.logMsgWidth()
	if got := a.logMessageLines(big, w); strings.Join(got, "\n") != strings.Join(wrapPlain(big, w), "\n") {
		t.Error("oversized markdown should plain-wrap")
	}
	if _, ok := a.logMDCache[big]; !ok {
		t.Error("oversized message should cache its plain wrap")
	}
	if a.warmMarkdownCmd([]LogEntry{{Message: big}}) != nil {
		t.Error("warm-up must skip oversized messages")
	}
}

// Data-arrival handlers kick the warm-up so styled markdown follows the
// instant plain paint without any user action.
func TestLogDataMessagesTriggerWarm(t *testing.T) {
	md := "## Turn\n\n- did a thing\n"
	entries := []LogEntry{{Message: md}}

	a := newTestApp(90, 24)
	if _, cmd := a.Update(taskLogsMsg{taskID: "T-1", logs: entries}); cmd == nil {
		t.Error("taskLogsMsg should trigger a warm-up")
	}

	a = newTestApp(90, 24)
	if _, cmd := a.Update(agentTaskLogsMsg{taskID: "T-1", logs: entries}); cmd == nil {
		t.Error("agentTaskLogsMsg should trigger a warm-up")
	}

	a = newTestApp(90, 24)
	if _, cmd := a.Update(logsDataMsg{logs: entries}); cmd == nil {
		t.Error("logsDataMsg should trigger a warm-up")
	}

	a = newTestApp(90, 24)
	segs := []TaskHistorySegmentItem{{Logs: entries}}
	if _, cmd := a.Update(taskHistoryMsg{taskID: "it-1", segments: segs}); cmd == nil {
		t.Error("taskHistoryMsg should trigger a warm-up")
	}
}
