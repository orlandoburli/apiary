package execution

import (
	"strings"
	"testing"

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

	cleaned, structured, summary, _, _ := extractStructured(raw)

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
	cleaned, structured, summary, publish, _ := extractStructured(raw)
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
	_, structured, _, _, _ := extractStructured(raw)
	if structured == nil || structured["v"] != float64(2) {
		t.Errorf("expected last APIARY_OUTPUT to win, got: %#v", structured)
	}
}

func TestExtractStructured_BareLineRegression(t *testing.T) {
	raw := strings.Join([]string{
		"real output",
		`APIARY_OUTPUT: {"review_verdict":"rejected","reason":"missing tests"}`,
	}, "\n")
	cleaned, structured, _, _, _ := extractStructured(raw)
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
	cleaned, structured, _, _, _ := extractStructured(raw)
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
	_, structured, _, _, _ := extractStructured(raw)
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
	_, structured, _, _, _ := extractStructured(raw)
	if structured == nil {
		t.Fatal("expected structured output, got nil")
	}
	if structured["review_verdict"] != "rejected" {
		t.Errorf("expected last (wrapped) APIARY_OUTPUT to win, got: %#v", structured)
	}
}

func TestExtractStructured_ProseMentionNotStripped(t *testing.T) {
	raw := "I will now emit APIARY_OUTPUT: with the final verdict."
	cleaned, structured, _, _, _ := extractStructured(raw)
	if structured != nil {
		t.Errorf("prose mention should not parse as structured, got: %#v", structured)
	}
	if cleaned != raw {
		t.Errorf("prose line mentioning the sentinel should be preserved, got: %q", cleaned)
	}
}

func TestExtractStructured_InvalidJSONStrippedButNil(t *testing.T) {
	raw := "real output\nAPIARY_OUTPUT: {not valid json}"
	cleaned, structured, _, _, _ := extractStructured(raw)
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

	cleaned, structured, summary, publish, _ := extractStructured(raw)

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
	if !strings.HasPrefix(prompt, "Task: Plain task") {
		t.Errorf("plain prompt should start with the task line, got:\n%s", prompt)
	}
}
