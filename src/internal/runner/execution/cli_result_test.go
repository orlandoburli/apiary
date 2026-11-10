package execution

import (
	"context"
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/model"
)

// TestResultOutputWithSentinel_PrefersEarlierStructuredResult is the #528
// regression: a background task wakes the session after the agent's answer and
// the CLI emits a second result with no sentinel. The first result's structured
// output and its summary must survive.
func TestResultOutputWithSentinel_PrefersEarlierStructuredResult(t *testing.T) {
	first := strings.Join([]string{
		"APIARY_SUMMARY_START",
		"- implemented the fix",
		"- tests pass",
		"APIARY_SUMMARY_END",
		`APIARY_OUTPUT: {"ok":true,"action":"implement"}`,
	}, "\n")
	second := "Background task finished; nothing left to do."

	chosen := resultOutputWithSentinel([]string{first, second})
	if chosen != first {
		t.Fatalf("chose %q, want the first result", chosen)
	}

	result := model.RunResult{Output: chosen}
	applyStructured(&result)
	if result.StructuredOutput == nil || result.StructuredOutput["action"] != "implement" {
		t.Errorf("structured output lost: %#v", result.StructuredOutput)
	}
	if !strings.Contains(result.Summary, "implemented the fix") || !strings.Contains(result.Summary, "tests pass") {
		t.Errorf("summary lost: %q", result.Summary)
	}
}

// The normal case must keep working: the last result carrying a valid sentinel
// wins, so an agent that revises its verdict is still honoured.
func TestResultOutputWithSentinel_LastValidResultWins(t *testing.T) {
	first := `APIARY_OUTPUT: {"v":1}`
	last := `APIARY_OUTPUT: {"v":2}`

	chosen := resultOutputWithSentinel([]string{first, last})
	if chosen != last {
		t.Fatalf("chose %q, want the last result %q", chosen, last)
	}
}

// A malformed sentinel is not a valid APIARY_OUTPUT: it must not shadow an
// earlier result that carried the real one.
func TestResultOutputWithSentinel_MalformedSentinelDoesNotShadow(t *testing.T) {
	good := `APIARY_OUTPUT: {"v":1}`
	malformed := "Background task finished.\nAPIARY_OUTPUT: {oops"

	chosen := resultOutputWithSentinel([]string{good, malformed})
	if chosen != good {
		t.Fatalf("chose %q, want the earlier valid result %q", chosen, good)
	}
}

// With no sentinel anywhere, the last non-empty result still wins — the
// behaviour before #528.
func TestResultOutputWithSentinel_NoSentinelFallsBackToLastText(t *testing.T) {
	chosen := resultOutputWithSentinel([]string{"first answer", "   ", "second answer"})
	if chosen != "second answer" {
		t.Fatalf("chose %q, want the last non-empty result", chosen)
	}
}

// No usable result text (and no result event at all) must return "", which the
// runner reads as "fall back to the full stdout".
func TestResultOutputWithSentinel_NoUsableResults(t *testing.T) {
	if got := resultOutputWithSentinel(nil); got != "" {
		t.Errorf("no results = %q, want empty", got)
	}
	if got := resultOutputWithSentinel([]string{"", "  "}); got != "" {
		t.Errorf("blank results = %q, want empty", got)
	}
}

// End-to-end over a real subprocess: the scanner must collect both result
// events and the runner must record the first one's structured output.
func TestRun_KeepsStructuredOutputWhenLaterResultHasNone(t *testing.T) {
	script := `printf '%s\n' \
  '{"type":"system","subtype":"init"}' \
  '{"type":"result","subtype":"success","result":"APIARY_SUMMARY_START\n- shaped the fix\nAPIARY_SUMMARY_END\nAPIARY_OUTPUT: {\"ok\":true,\"answer\":42}"}' \
  '{"type":"result","subtype":"success","result":"Background task finished."}'`
	r := shellRunner(t, script)

	res, err := r.Run(context.Background(), model.RunRequest{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Success {
		t.Fatalf("run should succeed, got error %v", res.Error)
	}
	if res.StructuredOutput == nil {
		t.Fatal("structured output lost after the background result")
	}
	if res.StructuredOutput["ok"] != true || res.StructuredOutput["answer"] != float64(42) {
		t.Errorf("structured output parsed incorrectly: %#v", res.StructuredOutput)
	}
	if !strings.Contains(res.Summary, "shaped the fix") {
		t.Errorf("summary lost: %q", res.Summary)
	}
	if strings.Contains(res.Output, "Background task finished.") {
		t.Errorf("the sentinel-less follow-up leaked into the output: %q", res.Output)
	}
}

// Cursor-cli shares CliRunner but emits "completion" events instead of
// "result"; it is affected by the same bug and covered by the same selection.
func TestRun_KeepsStructuredOutputWhenLaterCompletionHasNone(t *testing.T) {
	script := `printf '%s\n' \
  '{"type":"system","subtype":"init"}' \
  '{"type":"completion","result":"APIARY_OUTPUT: {\"verdict\":\"approved\"}"}' \
  '{"type":"completion","result":"Background task finished."}'`
	r := shellRunner(t, script)

	res, err := r.Run(context.Background(), model.RunRequest{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Success {
		t.Fatalf("run should succeed, got error %v", res.Error)
	}
	if res.StructuredOutput == nil || res.StructuredOutput["verdict"] != "approved" {
		t.Fatalf("structured output lost after the background completion: %#v", res.StructuredOutput)
	}
	if strings.Contains(res.Output, "Background task finished.") {
		t.Errorf("the sentinel-less follow-up leaked into the output: %q", res.Output)
	}
}

// End-to-end: a session with no result event still records the full stdout.
func TestRun_NoResultEventKeepsFullStdout(t *testing.T) {
	r := shellRunner(t, `printf '%s\n' 'hello from stdout' 'second line'`)

	res, err := r.Run(context.Background(), model.RunRequest{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := "hello from stdout\nsecond line"; res.Output != want {
		t.Errorf("output = %q, want %q", res.Output, want)
	}
}
