package execution

import (
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
)

func TestExtractStructured_OutputAndSummary(t *testing.T) {
	raw := strings.Join([]string{
		"I analyzed the task and here is my work.",
		"Made some changes.",
		"APIARY_SUMMARY_START",
		"- Refactored the auth middleware",
		"- No blockers",
		"APIARY_SUMMARY_END",
		`APIARY_OUTPUT: {"complexity":"high","action":"implement"}`,
	}, "\n")

	cleaned, structured, summary, _, _, _ := extractStructured(raw)

	if strings.Contains(cleaned, "APIARY_OUTPUT") || strings.Contains(cleaned, "APIARY_SUMMARY") {
		t.Errorf("cleaned output still contains sentinels:\n%s", cleaned)
	}
	if !strings.Contains(cleaned, "I analyzed the task") || !strings.Contains(cleaned, "Made some changes.") {
		t.Errorf("cleaned output dropped human text:\n%s", cleaned)
	}
	if structured == nil {
		t.Fatal("expected structured output, got nil")
	}
	if structured["complexity"] != "high" || structured["action"] != "implement" {
		t.Errorf("structured parsed incorrectly: %#v", structured)
	}
	if !strings.Contains(summary, "Refactored the auth middleware") || !strings.Contains(summary, "No blockers") {
		t.Errorf("summary parsed incorrectly: %q", summary)
	}
}

func TestExtractStructured_NoSentinels(t *testing.T) {
	raw := "Just a normal agent response.\nNothing structured here."
	cleaned, structured, summary, publish, _, _ := extractStructured(raw)
	if publish != "" {
		t.Errorf("expected empty publish, got: %q", publish)
	}
	if cleaned != raw {
		t.Errorf("cleaned should be unchanged, got: %q", cleaned)
	}
	if structured != nil {
		t.Errorf("expected nil structured, got: %#v", structured)
	}
	if summary != "" {
		t.Errorf("expected empty summary, got: %q", summary)
	}
}

func TestExtractStructured_LastOutputWins(t *testing.T) {
	raw := strings.Join([]string{
		`APIARY_OUTPUT: {"v":1}`,
		"some text",
		`APIARY_OUTPUT: {"v":2}`,
	}, "\n")
	_, structured, _, _, _, _ := extractStructured(raw)
	if structured == nil || structured["v"] != float64(2) {
		t.Errorf("expected last APIARY_OUTPUT to win, got: %#v", structured)
	}
}

func TestExtractStructured_BareLineRegression(t *testing.T) {
	raw := strings.Join([]string{
		"real output",
		`APIARY_OUTPUT: {"review_verdict":"rejected","reason":"missing tests"}`,
	}, "\n")
	cleaned, structured, _, _, _, _ := extractStructured(raw)
	if structured == nil {
		t.Fatal("expected structured output for bare sentinel, got nil")
	}
	if structured["review_verdict"] != "rejected" {
		t.Errorf("structured parsed incorrectly: %#v", structured)
	}
	if strings.Contains(cleaned, "APIARY_OUTPUT") {
		t.Errorf("sentinel line not stripped:\n%s", cleaned)
	}
	if cleaned != "real output" {
		t.Errorf("unexpected cleaned output: %q", cleaned)
	}
}

func TestExtractStructured_InlineBacktickWrapped(t *testing.T) {
	raw := strings.Join([]string{
		"Here is my verdict:",
		"`APIARY_OUTPUT: {\"review_verdict\": \"rejected\", \"reason\": \"flaky test\"}`",
	}, "\n")
	cleaned, structured, _, _, _, _ := extractStructured(raw)
	if structured == nil {
		t.Fatal("expected structured output for backtick-wrapped sentinel, got nil")
	}
	if structured["review_verdict"] != "rejected" {
		t.Errorf("verdict lost when wrapped in backticks: %#v", structured)
	}
	if structured["reason"] != "flaky test" {
		t.Errorf("reason parsed incorrectly: %#v", structured)
	}
	if strings.Contains(cleaned, "APIARY_OUTPUT") || strings.Contains(cleaned, "review_verdict") {
		t.Errorf("backtick-wrapped sentinel not stripped:\n%s", cleaned)
	}
	if cleaned != "Here is my verdict:" {
		t.Errorf("unexpected cleaned output: %q", cleaned)
	}
}

func TestExtractStructured_FencedCodeBlock(t *testing.T) {
	raw := strings.Join([]string{
		"Result below.",
		"```json",
		`APIARY_OUTPUT: {"review_verdict":"approved"}`,
		"```",
	}, "\n")
	_, structured, _, _, _, _ := extractStructured(raw)
	if structured == nil {
		t.Fatal("expected structured output inside fenced block, got nil")
	}
	if structured["review_verdict"] != "approved" {
		t.Errorf("verdict lost inside fenced block: %#v", structured)
	}
}

func TestExtractStructured_WrappedLastWins(t *testing.T) {
	raw := strings.Join([]string{
		`APIARY_OUTPUT: {"review_verdict":"approved"}`,
		"some reconsideration",
		"`APIARY_OUTPUT: {\"review_verdict\": \"rejected\"}`",
	}, "\n")
	_, structured, _, _, _, _ := extractStructured(raw)
	if structured == nil {
		t.Fatal("expected structured output, got nil")
	}
	if structured["review_verdict"] != "rejected" {
		t.Errorf("expected last (wrapped) APIARY_OUTPUT to win, got: %#v", structured)
	}
}

func TestExtractStructured_ProseMentionNotStripped(t *testing.T) {
	raw := "I will now emit APIARY_OUTPUT: with the final verdict."
	cleaned, structured, _, _, _, _ := extractStructured(raw)
	if structured != nil {
		t.Errorf("prose mention should not parse as structured, got: %#v", structured)
	}
	if cleaned != raw {
		t.Errorf("prose line mentioning the sentinel should be preserved, got: %q", cleaned)
	}
}

func TestExtractStructured_InvalidJSONStrippedButNil(t *testing.T) {
	raw := "real output\nAPIARY_OUTPUT: {not valid json}"
	cleaned, structured, _, _, _, _ := extractStructured(raw)
	if structured != nil {
		t.Errorf("expected nil structured for invalid JSON, got: %#v", structured)
	}
	if strings.Contains(cleaned, "APIARY_OUTPUT") {
		t.Errorf("invalid sentinel line should still be stripped:\n%s", cleaned)
	}
	if cleaned != "real output" {
		t.Errorf("unexpected cleaned output: %q", cleaned)
	}
}

func TestExtractStructured_PublishBlock(t *testing.T) {
	raw := strings.Join([]string{
		"Working on it.",
		"APIARY_PUBLISH_BEGIN",
		"## Result",
		"Done. See the diff.",
		"APIARY_PUBLISH_END",
		"Trailing line.",
	}, "\n")

	cleaned, structured, summary, publish, _, _ := extractStructured(raw)

	if structured != nil || summary != "" {
		t.Errorf("expected no structured/summary, got %#v / %q", structured, summary)
	}
	if strings.Contains(cleaned, "APIARY_PUBLISH") || strings.Contains(cleaned, "## Result") {
		t.Errorf("publish block not stripped from cleaned output:\n%s", cleaned)
	}
	if !strings.Contains(cleaned, "Working on it.") || !strings.Contains(cleaned, "Trailing line.") {
		t.Errorf("cleaned output dropped human text:\n%s", cleaned)
	}
	if publish != "## Result\nDone. See the diff." {
		t.Errorf("publish payload parsed incorrectly: %q", publish)
	}
}

func TestApplyStructured_SpawnBlock(t *testing.T) {
	result := model.RunResult{
		Output: strings.Join([]string{
			"Decided to delegate.",
			"APIARY_SPAWN_BEGIN",
			`{"workflow":"collect-logs","title":"Collect logs","input":{"severity":"high"}}`,
			"APIARY_SPAWN_END",
		}, "\n"),
	}
	applyStructured(&result)

	if result.Output != "Decided to delegate." {
		t.Errorf("spawn block not stripped from output: %q", result.Output)
	}
	if result.SpawnError != nil {
		t.Fatalf("unexpected spawn error: %v", result.SpawnError)
	}
	if result.SpawnRequest == nil {
		t.Fatal("expected spawn request, got nil")
	}
	if result.SpawnRequest.WorkflowID != "collect-logs" || result.SpawnRequest.Title != "Collect logs" {
		t.Errorf("spawn request parsed incorrectly: %#v", result.SpawnRequest)
	}
	if result.SpawnRequest.Input["severity"] != "high" {
		t.Errorf("spawn input parsed incorrectly: %#v", result.SpawnRequest.Input)
	}
	// ParentTaskID is never taken from agent output (json:"-").
	if result.SpawnRequest.ParentTaskID != "" {
		t.Errorf("ParentTaskID should not be set from agent output, got %q", result.SpawnRequest.ParentTaskID)
	}
}

func TestApplyStructured_SpawnInvalidJSON(t *testing.T) {
	result := model.RunResult{
		Output: "APIARY_SPAWN_BEGIN\n{not valid json}\nAPIARY_SPAWN_END",
	}
	applyStructured(&result)

	if result.SpawnRequest != nil {
		t.Errorf("expected nil spawn request for invalid JSON, got %#v", result.SpawnRequest)
	}
	if result.SpawnError == nil {
		t.Fatal("expected spawn error for invalid JSON, got nil")
	}
}

// TestApplyStructured_SpawnArray covers a decomposition emitting several children
// in one APIARY_SPAWN block: a JSON array parses into SpawnRequests (not the
// single SpawnRequest), preserving each entry's materialize fields.
func TestApplyStructured_SpawnArray(t *testing.T) {
	result := model.RunResult{
		Output: strings.Join([]string{
			"Decomposed.",
			"APIARY_SPAWN_BEGIN",
			`[`,
			`  {"title":"Backend","body":"spec b","labels":["agent:backend"],"key":"be"},`,
			`  {"title":"Frontend","body":"spec f","labels":["agent:frontend"],"key":"fe"}`,
			`]`,
			"APIARY_SPAWN_END",
		}, "\n"),
	}
	applyStructured(&result)

	if result.SpawnError != nil {
		t.Fatalf("unexpected spawn error: %v", result.SpawnError)
	}
	if result.SpawnRequest != nil {
		t.Errorf("array form must not populate single SpawnRequest, got %#v", result.SpawnRequest)
	}
	if len(result.SpawnRequests) != 2 {
		t.Fatalf("expected 2 spawn requests, got %d: %#v", len(result.SpawnRequests), result.SpawnRequests)
	}
	be := result.SpawnRequests[0]
	if be.Title != "Backend" || be.Body != "spec b" || be.Key != "be" || be.WorkflowID != "" {
		t.Errorf("first request parsed incorrectly: %#v", be)
	}
	if len(be.Labels) != 1 || be.Labels[0] != "agent:backend" {
		t.Errorf("first request labels = %v, want [agent:backend]", be.Labels)
	}
	if result.SpawnRequests[1].Key != "fe" {
		t.Errorf("second request key = %q, want fe", result.SpawnRequests[1].Key)
	}
}

func TestApplyStructured_MutatesResult(t *testing.T) {
	result := model.RunResult{
		Output: "done\nAPIARY_OUTPUT: {\"ok\":true}\nAPIARY_PUBLISH_BEGIN\nposted\nAPIARY_PUBLISH_END",
	}
	applyStructured(&result)
	if result.Output != "done" {
		t.Errorf("output not cleaned: %q", result.Output)
	}
	if result.StructuredOutput == nil || result.StructuredOutput["ok"] != true {
		t.Errorf("structured not populated: %#v", result.StructuredOutput)
	}
	if result.PublishPayload != "posted" {
		t.Errorf("publish payload not populated: %q", result.PublishPayload)
	}
}

func TestBuildPrompt_PrependAndSummary(t *testing.T) {
	req := model.RunRequest{
		Cell:          model.SourceItem{Title: "Fix bug"},
		SystemPrepend: "=== Workflow Memory ===\ncomplexity: high\n======================",
		SystemAppend:  "soul content",
		SummaryPrompt: "Summarize what you did.",
	}
	prompt := buildPrompt(req)

	if !strings.HasPrefix(prompt, "=== Workflow Memory ===") {
		t.Errorf("expected memory prepended at start, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Task: Fix bug") {
		t.Error("expected cell title in prompt")
	}
	if !strings.Contains(prompt, "soul content") {
		t.Error("expected SystemAppend in prompt")
	}
	if !strings.Contains(prompt, summaryStartMarker) || !strings.Contains(prompt, summaryEndMarker) {
		t.Error("expected summary markers in prompt")
	}
	if !strings.Contains(prompt, "Summarize what you did.") {
		t.Error("expected summary prompt text")
	}
}

func TestBuildPrompt_NoWorkflowFieldsUnchanged(t *testing.T) {
	req := model.RunRequest{Cell: model.SourceItem{Title: "Plain task"}}
	prompt := buildPrompt(req)
	if strings.Contains(prompt, summaryStartMarker) {
		t.Error("plain prompt should not contain summary markers")
	}
	// Ticket content is now wrapped in the untrusted-content block; the task
	// line sits inside it rather than at the very start.
	if !strings.Contains(prompt, "Task: Plain task") {
		t.Errorf("plain prompt should contain the task line, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "<<<"+untrustedToken) {
		t.Errorf("plain prompt should wrap ticket content in the untrusted block, got:\n%s", prompt)
	}
}

func TestApplyStructured_MemorizeBlock(t *testing.T) {
	result := model.RunResult{Output: "done\n" +
		"APIARY_MEMORIZE_BEGIN\n" +
		`{"scope":"global","name":"ci-duration","description":"CI takes 12m","content":"plan accordingly"}` + "\n" +
		"APIARY_MEMORIZE_END\n"}
	applyStructured(&result)

	if result.MemorizeError != nil {
		t.Fatalf("unexpected error: %v", result.MemorizeError)
	}
	if len(result.MemorizeRequests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(result.MemorizeRequests))
	}
	req := result.MemorizeRequests[0]
	if req.Scope != "global" || req.Name != "ci-duration" || req.Content != "plan accordingly" {
		t.Fatalf("unexpected request: %+v", req)
	}
	if strings.Contains(result.Output, "APIARY_MEMORIZE") {
		t.Fatalf("markers not stripped: %q", result.Output)
	}
	if result.Output != "done" {
		t.Fatalf("unexpected cleaned output: %q", result.Output)
	}
}

func TestApplyStructured_MemorizeArrayAndDefaults(t *testing.T) {
	result := model.RunResult{Output: "APIARY_MEMORIZE_BEGIN\n" +
		`[{"content":"task note, no scope"},{"scope":"global","name":"x-y","description":"d","content":"c"}]` + "\n" +
		"APIARY_MEMORIZE_END"}
	applyStructured(&result)

	if result.MemorizeError != nil {
		t.Fatalf("unexpected error: %v", result.MemorizeError)
	}
	if len(result.MemorizeRequests) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(result.MemorizeRequests))
	}
	if result.MemorizeRequests[0].Scope != "" {
		t.Fatalf("scope should stay empty (task default applied later): %+v", result.MemorizeRequests[0])
	}
}

func TestApplyStructured_MemorizeInvalidJSON(t *testing.T) {
	result := model.RunResult{Output: "ok\nAPIARY_MEMORIZE_BEGIN\nnot json\nAPIARY_MEMORIZE_END"}
	applyStructured(&result)

	if result.MemorizeError == nil {
		t.Fatal("expected MemorizeError")
	}
	if len(result.MemorizeRequests) != 0 {
		t.Fatalf("no requests expected, got %v", result.MemorizeRequests)
	}
	// The block is stripped even when malformed, so it never leaks downstream.
	if strings.Contains(result.Output, "APIARY_MEMORIZE") || strings.Contains(result.Output, "not json") {
		t.Fatalf("block not stripped: %q", result.Output)
	}
}

func TestOutputSchemaInstruction(t *testing.T) {
	schema := &config.OutputSchema{Type: "object",
		Properties: map[string]config.SchemaField{
			"action": {Type: "string", Enum: []string{"pr_opened", "already_done"}},
			"reason": {Type: "string"},
		},
		Required: []string{"action"}}

	got := OutputSchemaInstruction(schema)
	for _, want := range []string{
		"APIARY_OUTPUT:",
		`"action": "pr_opened|already_done"`,
		"- action (string, one of: pr_opened | already_done, required)",
		"- reason (string)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("instruction missing %q:\n%s", want, got)
		}
	}
	// Required fields come before optional ones in the example line.
	if strings.Index(got, `"action"`) > strings.Index(got, `"reason"`) {
		t.Errorf("required field should precede optional in example:\n%s", got)
	}
	if OutputSchemaInstruction(nil) != "" {
		t.Error("nil schema must render no instruction")
	}
}

func TestBuildPrompt_IncludesOutputInstruction(t *testing.T) {
	req := model.RunRequest{
		Cell:              model.SourceItem{Title: "issue"},
		OutputInstruction: "\n---\nAPIARY_OUTPUT: {\"action\": \"...\"}\n",
	}
	got := buildPrompt(req)
	if !strings.Contains(got, `APIARY_OUTPUT: {"action": "..."}`) {
		t.Errorf("prompt missing output instruction:\n%s", got)
	}
}

// fiveSpawnBlocks is the output shape from issue #494: one APIARY_SPAWN block
// per child, each with its own dedup key, separated by blank lines and prose.
const fiveSpawnBlocks = `Found five stale tasks.

APIARY_SPAWN_BEGIN
{"workflow": "unblock", "title": "Unblock #4450", "input": {"issue": 4450}, "key": "unblock/needs_human_stale/4450"}
APIARY_SPAWN_END

APIARY_SPAWN_BEGIN
{"workflow": "unblock", "title": "Unblock #4940", "input": {"issue": 4940}, "key": "unblock/needs_human_stale/4940"}
APIARY_SPAWN_END

APIARY_SPAWN_BEGIN
{"workflow": "unblock", "title": "Unblock #5001", "input": {"issue": 5001}, "key": "unblock/needs_human_stale/5001"}
APIARY_SPAWN_END

Two more:

APIARY_SPAWN_BEGIN
{"workflow": "unblock", "title": "Unblock #5002", "input": {"issue": 5002}, "key": "unblock/needs_human_stale/5002"}
APIARY_SPAWN_END

APIARY_SPAWN_BEGIN
{"workflow": "unblock", "title": "Unblock #5003", "input": {"issue": 5003}, "key": "unblock/needs_human_stale/5003"}
APIARY_SPAWN_END`

// TestApplyStructured_SpawnMultipleBlocks is the #494 regression: several
// blocks used to be concatenated into one payload, which failed as invalid JSON
// and created no child at all.
func TestApplyStructured_SpawnMultipleBlocks(t *testing.T) {
	result := model.RunResult{Output: fiveSpawnBlocks}
	applyStructured(&result)

	if result.SpawnError != nil {
		t.Fatalf("unexpected spawn error: %v", result.SpawnError)
	}
	if result.SpawnRequest != nil {
		t.Errorf("multiple blocks must not populate single SpawnRequest, got %#v", result.SpawnRequest)
	}
	wantKeys := []string{
		"unblock/needs_human_stale/4450",
		"unblock/needs_human_stale/4940",
		"unblock/needs_human_stale/5001",
		"unblock/needs_human_stale/5002",
		"unblock/needs_human_stale/5003",
	}
	if len(result.SpawnRequests) != len(wantKeys) {
		t.Fatalf("expected %d spawn requests, got %d: %#v", len(wantKeys), len(result.SpawnRequests), result.SpawnRequests)
	}
	for i, want := range wantKeys {
		req := result.SpawnRequests[i]
		if req.Key != want {
			t.Errorf("request %d key = %q, want %q", i, req.Key, want)
		}
		if req.WorkflowID != "unblock" {
			t.Errorf("request %d workflow = %q, want unblock", i, req.WorkflowID)
		}
	}
	if want := "Found five stale tasks.\n\n\n\n\nTwo more:"; result.Output != want {
		t.Errorf("prose between blocks must survive, markers must not: %q", result.Output)
	}
}

// TestApplyStructured_SpawnMixedObjectAndArrayBlocks: each block keeps its own
// form, so an object block and an array block add up.
func TestApplyStructured_SpawnMixedObjectAndArrayBlocks(t *testing.T) {
	result := model.RunResult{Output: `APIARY_SPAWN_BEGIN
{"title": "One", "key": "one"}
APIARY_SPAWN_END
APIARY_SPAWN_BEGIN
[{"title": "Two", "key": "two"}, {"title": "Three", "key": "three"}]
APIARY_SPAWN_END`}
	applyStructured(&result)

	if result.SpawnError != nil {
		t.Fatalf("unexpected spawn error: %v", result.SpawnError)
	}
	var keys []string
	for _, req := range result.SpawnRequests {
		keys = append(keys, req.Key)
	}
	if got := strings.Join(keys, ","); got != "one,two,three" {
		t.Errorf("keys = %q, want one,two,three", got)
	}
}

// TestApplyStructured_SpawnOneBadBlockSpawnsNothing pins the partial-validity
// rule: one malformed block fails the whole spawn. A retry re-emits every
// block, and a request with no explicit key derives its dedup key from a title
// the agent may reword, so spawning the valid part first risks duplicates.
func TestApplyStructured_SpawnOneBadBlockSpawnsNothing(t *testing.T) {
	result := model.RunResult{Output: `APIARY_SPAWN_BEGIN
{"workflow": "unblock", "key": "a"}
APIARY_SPAWN_END
APIARY_SPAWN_BEGIN
{not valid json}
APIARY_SPAWN_END
APIARY_SPAWN_BEGIN
{"workflow": "unblock", "key": "c"}
APIARY_SPAWN_END`}
	applyStructured(&result)

	if result.SpawnRequest != nil || len(result.SpawnRequests) != 0 {
		t.Errorf("no request may survive a malformed block: %#v %#v", result.SpawnRequest, result.SpawnRequests)
	}
	if result.SpawnError == nil {
		t.Fatal("expected spawn error")
	}
	if msg := result.SpawnError.Error(); !strings.Contains(msg, "APIARY_SPAWN") || !strings.Contains(msg, "block 2 of 3") {
		t.Errorf("error must name the marker and the bad block, got %q", msg)
	}
}

// TestApplyStructured_SpawnEveryBadBlockReported: an agent fixing its output
// should learn about all the bad blocks from one failure.
func TestApplyStructured_SpawnEveryBadBlockReported(t *testing.T) {
	result := model.RunResult{Output: `APIARY_SPAWN_BEGIN
{bad}
APIARY_SPAWN_END
APIARY_SPAWN_BEGIN
[{bad}]
APIARY_SPAWN_END`}
	applyStructured(&result)

	if result.SpawnError == nil {
		t.Fatal("expected spawn error")
	}
	msg := result.SpawnError.Error()
	for _, want := range []string{"block 1 of 2: invalid JSON", "block 2 of 2: invalid JSON array"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q: %q", want, msg)
		}
	}
}

// TestApplyStructured_SpawnEmptyBlockIgnored: an empty block carries no request
// and is not an error, alone or next to a real one.
func TestApplyStructured_SpawnEmptyBlockIgnored(t *testing.T) {
	alone := model.RunResult{Output: "APIARY_SPAWN_BEGIN\n\nAPIARY_SPAWN_END"}
	applyStructured(&alone)
	if alone.SpawnError != nil || alone.SpawnRequest != nil || len(alone.SpawnRequests) != 0 {
		t.Errorf("empty block must be a no-op: %+v", alone)
	}

	withReal := model.RunResult{Output: "APIARY_SPAWN_BEGIN\nAPIARY_SPAWN_END\nAPIARY_SPAWN_BEGIN\n{\"key\": \"a\"}\nAPIARY_SPAWN_END"}
	applyStructured(&withReal)
	if withReal.SpawnError != nil {
		t.Fatalf("unexpected spawn error: %v", withReal.SpawnError)
	}
	if withReal.SpawnRequest == nil || withReal.SpawnRequest.Key != "a" {
		t.Errorf("one real block keeps the single form: %+v", withReal)
	}
}

// TestApplyStructured_MemorizeMultipleBlocks: APIARY_MEMORIZE shared the #494
// concatenation, so two blocks lost both memories to a parse warning.
func TestApplyStructured_MemorizeMultipleBlocks(t *testing.T) {
	result := model.RunResult{Output: `APIARY_MEMORIZE_BEGIN
{"content": "first"}
APIARY_MEMORIZE_END
APIARY_MEMORIZE_BEGIN
[{"content": "second"}, {"content": "third"}]
APIARY_MEMORIZE_END`}
	applyStructured(&result)

	if result.MemorizeError != nil {
		t.Fatalf("unexpected error: %v", result.MemorizeError)
	}
	if len(result.MemorizeRequests) != 3 {
		t.Fatalf("expected 3 requests, got %d", len(result.MemorizeRequests))
	}
	if result.MemorizeRequests[2].Content != "third" {
		t.Errorf("requests parsed incorrectly: %+v", result.MemorizeRequests)
	}
}

// TestApplyStructured_MemorizeBadBlockKeepsTheRest: unlike spawn, memorize keeps
// the valid blocks. Entries are independent and a bad block only ever warns, so
// dropping good knowledge alongside it would help nobody.
func TestApplyStructured_MemorizeBadBlockKeepsTheRest(t *testing.T) {
	result := model.RunResult{Output: `APIARY_MEMORIZE_BEGIN
{"content": "kept"}
APIARY_MEMORIZE_END
APIARY_MEMORIZE_BEGIN
{not valid json}
APIARY_MEMORIZE_END`}
	applyStructured(&result)

	if result.MemorizeError == nil || !strings.Contains(result.MemorizeError.Error(), "block 2 of 2") {
		t.Errorf("expected error naming block 2, got %v", result.MemorizeError)
	}
	if len(result.MemorizeRequests) != 1 || result.MemorizeRequests[0].Content != "kept" {
		t.Errorf("valid block must survive: %+v", result.MemorizeRequests)
	}
}

// TestExtractStructured_MultipleTextBlocks: publish and summary are free text,
// so several blocks never failed, but they ran together into one paragraph.
func TestExtractStructured_MultipleTextBlocks(t *testing.T) {
	raw := `APIARY_PUBLISH_BEGIN
## First
APIARY_PUBLISH_END
APIARY_PUBLISH_BEGIN
second
APIARY_PUBLISH_END
APIARY_SUMMARY_START
one
APIARY_SUMMARY_END
APIARY_SUMMARY_START
two
APIARY_SUMMARY_END`
	_, _, summary, publish, _, _ := extractStructured(raw)
	if publish != "## First\n\nsecond" {
		t.Errorf("publish = %q", publish)
	}
	if summary != "one\n\ntwo" {
		t.Errorf("summary = %q", summary)
	}
}
