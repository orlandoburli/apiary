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

	cleaned, structured, summary := extractStructured(raw)

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
	cleaned, structured, summary := extractStructured(raw)
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
	_, structured, _ := extractStructured(raw)
	if structured == nil || structured["v"] != float64(2) {
		t.Errorf("expected last APIARY_OUTPUT to win, got: %#v", structured)
	}
}

func TestExtractStructured_InvalidJSONStrippedButNil(t *testing.T) {
	raw := "real output\nAPIARY_OUTPUT: {not valid json}"
	cleaned, structured, _ := extractStructured(raw)
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

func TestApplyStructured_MutatesResult(t *testing.T) {
	result := model.RunResult{
		Output: "done\nAPIARY_OUTPUT: {\"ok\":true}",
	}
	applyStructured(&result)
	if result.Output != "done" {
		t.Errorf("output not cleaned: %q", result.Output)
	}
	if result.StructuredOutput == nil || result.StructuredOutput["ok"] != true {
		t.Errorf("structured not populated: %#v", result.StructuredOutput)
	}
}

func TestBuildPrompt_PrependAndSummary(t *testing.T) {
	req := model.RunRequest{
		Cell:          model.Cell{Title: "Fix bug"},
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
	req := model.RunRequest{Cell: model.Cell{Title: "Plain task"}}
	prompt := buildPrompt(req)
	if strings.Contains(prompt, summaryStartMarker) {
		t.Error("plain prompt should not contain summary markers")
	}
	if !strings.HasPrefix(prompt, "Task: Plain task") {
		t.Errorf("plain prompt should start with the task line, got:\n%s", prompt)
	}
}
