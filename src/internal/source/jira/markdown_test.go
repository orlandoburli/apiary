package jira

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
)

func mustADF(t *testing.T, md string) string {
	t.Helper()
	blocks, ok := markdownToADF(md)
	if !ok {
		t.Fatalf("markdownToADF(%q) yielded nothing", md)
	}
	data, err := json.Marshal(adfDoc(blocks...))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(data)
}

func TestMarkdownToADF_EmptyYieldsNothing(t *testing.T) {
	for _, md := range []string{"", "   ", "\n\n\t"} {
		if blocks, ok := markdownToADF(md); ok {
			t.Errorf("%q: expected ok=false, got blocks %v", md, blocks)
		}
	}
}

func TestMarkdownToADF_HeadingAndInlineMarks(t *testing.T) {
	s := mustADF(t, "## Spec\n\nuse **bold**, *italic* and `go test` here")
	for _, want := range []string{
		`"type":"heading"`, `"level":2`, "Spec",
		`"type":"strong"`, "bold",
		`"type":"em"`, "italic",
		`"type":"code"`, "go test",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
	if strings.Contains(s, "**") || strings.Contains(s, "`") {
		t.Errorf("raw markdown syntax leaked into %s", s)
	}
}

func TestMarkdownToADF_Lists(t *testing.T) {
	s := mustADF(t, "- first\n- second\n\n1. one\n2. two")
	for _, want := range []string{
		`"type":"bulletList"`, `"type":"orderedList"`, `"type":"listItem"`,
		"first", "second", "one", "two",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
}

func TestMarkdownToADF_NestedList(t *testing.T) {
	s := mustADF(t, "- outer\n  - inner")
	outer := strings.Index(s, "outer")
	inner := strings.Index(s, "inner")
	if outer == -1 || inner == -1 || inner < outer {
		t.Fatalf("nested list order wrong: %s", s)
	}
	if strings.Count(s, `"type":"bulletList"`) != 2 {
		t.Errorf("expected two bulletLists in %s", s)
	}
}

func TestMarkdownToADF_FencedCodeBlock(t *testing.T) {
	s := mustADF(t, "```go\nfmt.Println(\"**not bold**\")\n```")
	for _, want := range []string{`"type":"codeBlock"`, `"language":"go"`, "**not bold**"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
	if strings.Contains(s, `"type":"strong"`) {
		t.Errorf("markdown inside fenced block must not be parsed: %s", s)
	}
}

func TestMarkdownToADF_LinksAndAutolink(t *testing.T) {
	s := mustADF(t, "see [the docs](https://example.com/docs) or https://example.com/raw")
	for _, want := range []string{
		`"type":"link"`, `"href":"https://example.com/docs"`, "the docs",
		`"href":"https://example.com/raw"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
}

func TestMarkdownToADF_BlockquoteAndRule(t *testing.T) {
	s := mustADF(t, "> quoted wisdom\n\n---\n\nafter")
	for _, want := range []string{`"type":"blockquote"`, "quoted wisdom", `"type":"rule"`, "after"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
}

func TestMarkdownToADF_Table(t *testing.T) {
	s := mustADF(t, "| Name | Role |\n|------|------|\n| Ada | engineer |")
	for _, want := range []string{
		`"type":"table"`, `"type":"tableRow"`,
		`"type":"tableHeader"`, "Name", "Role",
		`"type":"tableCell"`, "Ada", "engineer",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
}

func TestMarkdownToADF_TaskList(t *testing.T) {
	s := mustADF(t, "- [x] done thing\n- [ ] pending thing")
	for _, want := range []string{
		`"type":"taskList"`, `"type":"taskItem"`,
		`"state":"DONE"`, `"state":"TODO"`, `"localId"`,
		"done thing", "pending thing",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
	if strings.Contains(s, "[x]") || strings.Contains(s, "[ ]") {
		t.Errorf("checkbox syntax leaked as text: %s", s)
	}
}

func TestMarkdownToADF_MixedListNotTaskList(t *testing.T) {
	// One item lacks a checkbox, so the whole list falls back to a
	// bulletList and checkbox state is preserved as plain text.
	s := mustADF(t, "- [x] checked\n- plain item")
	if strings.Contains(s, "taskList") {
		t.Errorf("mixed list must not become taskList: %s", s)
	}
	for _, want := range []string{`"type":"bulletList"`, "[x] ", "checked", "plain item"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
}

func TestMarkdownToADF_HardAndSoftBreaks(t *testing.T) {
	s := mustADF(t, "hard  \nbreak and\nsoft break")
	if !strings.Contains(s, `"type":"hardBreak"`) {
		t.Errorf("missing hardBreak in %s", s)
	}
	blocks, _ := markdownToADF("hard  \nbreak and\nsoft break")
	text := adfToTextNodes(blocks)
	if !strings.Contains(strings.Join(text, ""), "and soft break") {
		t.Errorf("soft break must flatten to a space, got %q", text)
	}
}

// adfToTextNodes collects every text node's content, failing the walk if an
// empty text node is found (ADF rejects them).
func adfToTextNodes(blocks []adfNode) []string {
	var out []string
	var walk func(n adfNode)
	walk = func(n adfNode) {
		if n.Type == "text" {
			out = append(out, n.Text)
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	for _, b := range blocks {
		walk(b)
	}
	return out
}

func TestMarkdownToADF_NoEmptyTextNodes(t *testing.T) {
	md := "# H\n\npara **b** *i* `c` [l](http://x)\n\n- [x] t\n- [ ] u\n\n> q\n\n| a |\n|---|\n| b |\n\n```\ncode\n```\n\n---\n"
	blocks, ok := markdownToADF(md)
	if !ok {
		t.Fatal("conversion yielded nothing")
	}
	for _, text := range adfToTextNodes(blocks) {
		if text == "" {
			t.Fatal("found empty text node — ADF rejects these")
		}
	}
}

func TestFormatComment_MarkdownOutputRendersRich(t *testing.T) {
	result := model.RunResult{
		WorkerID: "engineer",
		Success:  true,
		Output:   "# Summary\n\nShipped the fix.\n\n- updated parser\n- added tests",
		Duration: time.Minute,
	}
	data, err := json.Marshal(formatComment(result))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	for _, want := range []string{
		`"type":"heading"`, "Summary",
		`"type":"bulletList"`, "updated parser", "added tests",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
	if strings.Contains(s, `"type":"codeBlock"`) {
		t.Errorf("markdown output must not be wrapped in a codeBlock: %s", s)
	}
}
