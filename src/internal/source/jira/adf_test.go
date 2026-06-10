package jira

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
)

func TestADFToText_EmptyAndNull(t *testing.T) {
	if got := adfToText(nil); got != "" {
		t.Errorf("nil: got %q", got)
	}
	if got := adfToText(json.RawMessage("null")); got != "" {
		t.Errorf("null: got %q", got)
	}
}

func TestADFToText_PlainString(t *testing.T) {
	if got := adfToText(json.RawMessage(`"just text"`)); got != "just text" {
		t.Errorf("got %q", got)
	}
}

func TestADFToText_ParagraphsAndHardBreak(t *testing.T) {
	doc := `{"type":"doc","version":1,"content":[
		{"type":"paragraph","content":[
			{"type":"text","text":"line one"},
			{"type":"hardBreak"},
			{"type":"text","text":"line two"}
		]},
		{"type":"paragraph","content":[{"type":"text","text":"second para"}]}
	]}`
	got := adfToText(json.RawMessage(doc))
	want := "line one\nline two\nsecond para"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestADFToText_CodeBlockAndList(t *testing.T) {
	doc := `{"type":"doc","version":1,"content":[
		{"type":"codeBlock","content":[{"type":"text","text":"go test ./..."}]},
		{"type":"bulletList","content":[
			{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"first"}]}]},
			{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"second"}]}]}
		]}
	]}`
	got := adfToText(json.RawMessage(doc))
	for _, want := range []string{"go test ./...", "first", "second"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestADFToText_MentionAndInlineCard(t *testing.T) {
	doc := `{"type":"doc","version":1,"content":[{"type":"paragraph","content":[
		{"type":"mention","attrs":{"id":"abc","text":"@Orlando"}},
		{"type":"text","text":" see "},
		{"type":"inlineCard","attrs":{"url":"https://example.com/spec"}}
	]}]}`
	got := adfToText(json.RawMessage(doc))
	want := "@Orlando see https://example.com/spec"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestADFToText_UnknownNodeRecursed(t *testing.T) {
	doc := `{"type":"doc","version":1,"content":[
		{"type":"futureWidget","content":[{"type":"text","text":"survives"}]}
	]}`
	if got := adfToText(json.RawMessage(doc)); got != "survives" {
		t.Errorf("got %q", got)
	}
}

func TestFormatComment_Success(t *testing.T) {
	result := model.RunResult{
		WorkerID: "engineer",
		Success:  true,
		Output:   "all done\nhttps://github.com/o/r/pull/7",
		Duration: 90 * time.Second,
	}
	data, err := json.Marshal(formatComment(result))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	for _, want := range []string{
		`"type":"doc"`, `"version":1,`,
		"Apiary run complete", "engineer", "1m30s",
		`"type":"paragraph"`, "all done",
		"https://github.com/o/r/pull/7", `"type":"link"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
}

func TestFormatComment_FailureWithError(t *testing.T) {
	result := model.RunResult{
		WorkerID: "qa",
		Success:  false,
		Error:    errors.New("exit status 1"),
	}
	data, err := json.Marshal(formatComment(result))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "Apiary run failed") || !strings.Contains(s, "exit status 1") {
		t.Errorf("failure layout wrong: %s", s)
	}
	if strings.Contains(s, "codeBlock") {
		t.Error("empty output must not produce a codeBlock (ADF rejects empty text nodes)")
	}
}

func TestFormatComment_EscapesSpecialChars(t *testing.T) {
	result := model.RunResult{
		WorkerID: "w",
		Success:  true,
		Output:   `quotes " and <pre> tags`,
	}
	data, err := json.Marshal(formatComment(result))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var roundTrip adfNode
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if got := adfToText(data); !strings.Contains(got, `quotes " and <pre> tags`) {
		t.Errorf("output not preserved verbatim: %q", got)
	}
}
