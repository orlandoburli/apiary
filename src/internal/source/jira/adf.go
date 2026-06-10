package jira

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
)

// adfNode is a single Atlassian Document Format node. Jira Cloud's v3 API
// represents rich text (descriptions, comments) as a tree of these.
type adfNode struct {
	Type    string         `json:"type"`
	Version int            `json:"version,omitempty"`
	Text    string         `json:"text,omitempty"`
	Content []adfNode      `json:"content,omitempty"`
	Marks   []adfMark      `json:"marks,omitempty"`
	Attrs   map[string]any `json:"attrs,omitempty"`
}

type adfMark struct {
	Type  string         `json:"type"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

// adfBlockNodes get a trailing newline when flattened to plain text.
var adfBlockNodes = map[string]bool{
	"paragraph":  true,
	"heading":    true,
	"codeBlock":  true,
	"blockquote": true,
	"listItem":   true,
	"tableRow":   true,
	"rule":       true,
}

// adfToText flattens an ADF document into plain text. It is tolerant of
// null/empty input, plain-string bodies, and unknown node types (which are
// recursed into so their text content is not lost).
func adfToText(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var n adfNode
	if err := json.Unmarshal(raw, &n); err != nil {
		return ""
	}
	var b strings.Builder
	flattenADF(&b, n)
	return strings.TrimSpace(b.String())
}

func flattenADF(b *strings.Builder, n adfNode) {
	switch n.Type {
	case "text":
		b.WriteString(n.Text)
	case "hardBreak":
		b.WriteString("\n")
	case "mention", "emoji":
		if t, ok := n.Attrs["text"].(string); ok {
			b.WriteString(t)
		}
	case "inlineCard":
		if u, ok := n.Attrs["url"].(string); ok {
			b.WriteString(u)
		}
	default:
		for _, c := range n.Content {
			flattenADF(b, c)
		}
		if adfBlockNodes[n.Type] {
			b.WriteString("\n")
		}
	}
}

// --- minimal ADF builders for WriteResult comments ---
//
// ADF rejects empty text nodes, so builders are only called with non-empty
// strings (callers guard).

func adfDoc(content ...adfNode) adfNode {
	return adfNode{Type: "doc", Version: 1, Content: content}
}

func adfParagraph(content ...adfNode) adfNode {
	return adfNode{Type: "paragraph", Content: content}
}

func adfText(s string, marks ...string) adfNode {
	n := adfNode{Type: "text", Text: s}
	for _, m := range marks {
		n.Marks = append(n.Marks, adfMark{Type: m})
	}
	return n
}

func adfLink(url string) adfNode {
	return adfNode{
		Type:  "text",
		Text:  url,
		Marks: []adfMark{{Type: "link", Attrs: map[string]any{"href": url}}},
	}
}

func adfCodeBlock(s string) adfNode {
	return adfNode{Type: "codeBlock", Content: []adfNode{{Type: "text", Text: s}}}
}

// formatComment renders a run result as an ADF document, mirroring the layout
// the other adapters post: status line, optional PR link, output, error.
func formatComment(result model.RunResult) adfNode {
	worker := result.WorkerID
	if worker == "" {
		worker = "unknown"
	}
	head := []adfNode{}
	if result.Success {
		head = append(head, adfText("✓ "), adfText("Apiary run complete", "strong"))
	} else {
		head = append(head, adfText("✗ "), adfText("Apiary run failed", "strong"))
	}
	head = append(head,
		adfText(" · worker: "),
		adfText(worker, "code"),
		adfText(fmt.Sprintf(" · duration: %s", result.Duration.Round(time.Second))),
	)

	blocks := []adfNode{adfParagraph(head...)}

	if prURL := extractPRURL(result.Output); prURL != "" {
		blocks = append(blocks, adfParagraph(adfText("🔗 Pull Request: ", "strong"), adfLink(prURL)))
	}
	if result.Output != "" {
		blocks = append(blocks, adfCodeBlock(result.Output))
	}
	if result.Error != nil && result.Error.Error() != "" {
		blocks = append(blocks, adfParagraph(adfText("Error: ", "strong"), adfText(result.Error.Error(), "code")))
	}
	return adfDoc(blocks...)
}

var prURLRe = regexp.MustCompile(`https://github\.com/[^/\s]+/[^/\s]+/pull/\d+`)

func extractPRURL(output string) string {
	if output == "" {
		return ""
	}
	if match := prURLRe.FindString(output); match != "" {
		return match
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "github.com") && strings.Contains(line, "/pull/") {
			return line
		}
	}
	return ""
}
