package jira

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// mdParser parses GitHub-flavored markdown (tables, strikethrough, task
// lists, bare-URL autolinks) — the dialect agents emit in APIARY_PUBLISH
// payloads and run output.
var mdParser = goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser()

// markdownToADF converts markdown into ADF block nodes so agent output
// renders as native Jira rich text instead of a monospace blob. It returns
// ok=false when nothing renderable came out (callers fall back to a plain
// codeBlock, the previous behavior).
func markdownToADF(md string) (blocks []adfNode, ok bool) {
	// Agent output is arbitrary; never let a converter bug take down the
	// comment write. The codeBlock fallback always works.
	defer func() {
		if recover() != nil {
			blocks, ok = nil, false
		}
	}()
	if strings.TrimSpace(md) == "" {
		return nil, false
	}
	c := &mdConverter{source: []byte(md)}
	doc := mdParser.Parse(text.NewReader(c.source))
	blocks = c.convertBlocks(doc)
	return blocks, len(blocks) > 0
}

type mdConverter struct {
	source []byte
	taskID int // deterministic localId counter for taskList/taskItem
}

// --- block conversion ---

func (c *mdConverter) convertBlocks(parent ast.Node) []adfNode {
	var blocks []adfNode
	for n := parent.FirstChild(); n != nil; n = n.NextSibling() {
		switch n := n.(type) {
		case *ast.Heading:
			if inl := c.convertInlines(n, nil); len(inl) > 0 {
				level := n.Level
				if level > 6 {
					level = 6
				}
				blocks = append(blocks, adfNode{
					Type:    "heading",
					Attrs:   map[string]any{"level": level},
					Content: inl,
				})
			}
		case *ast.Paragraph:
			if inl := c.convertInlines(n, nil); len(inl) > 0 {
				blocks = append(blocks, adfParagraph(inl...))
			}
		case *ast.TextBlock: // tight list items wrap inlines in a TextBlock
			if inl := c.convertInlines(n, nil); len(inl) > 0 {
				blocks = append(blocks, adfParagraph(inl...))
			}
		case *ast.FencedCodeBlock:
			block := adfNode{Type: "codeBlock"}
			if lang := string(n.Language(c.source)); lang != "" {
				block.Attrs = map[string]any{"language": lang}
			}
			if code := c.blockLines(n); code != "" {
				block.Content = []adfNode{{Type: "text", Text: code}}
			}
			blocks = append(blocks, block)
		case *ast.CodeBlock:
			block := adfNode{Type: "codeBlock"}
			if code := c.blockLines(n); code != "" {
				block.Content = []adfNode{{Type: "text", Text: code}}
			}
			blocks = append(blocks, block)
		case *ast.ThematicBreak:
			blocks = append(blocks, adfNode{Type: "rule"})
		case *ast.Blockquote:
			if inner := c.convertBlocks(n); len(inner) > 0 {
				blocks = append(blocks, adfNode{Type: "blockquote", Content: inner})
			}
		case *ast.List:
			if list, listOK := c.convertList(n); listOK {
				blocks = append(blocks, list)
			}
		case *ast.HTMLBlock:
			if raw := c.blockLines(n); raw != "" {
				blocks = append(blocks, adfParagraph(adfText(raw)))
			}
		case *extast.Table:
			if table, tableOK := c.convertTable(n); tableOK {
				blocks = append(blocks, table)
			}
		default:
			// Unknown block: recurse so nested content is not lost.
			blocks = append(blocks, c.convertBlocks(n)...)
		}
	}
	return blocks
}

// blockLines joins the raw source lines of a block node, without the
// trailing newline (Jira renders it as an extra blank line).
func (c *mdConverter) blockLines(n ast.Node) string {
	var b strings.Builder
	lines := n.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		b.Write(seg.Value(c.source))
	}
	return strings.TrimRight(b.String(), "\n")
}

func (c *mdConverter) convertList(n *ast.List) (adfNode, bool) {
	if c.isTaskList(n) {
		return c.convertTaskList(n)
	}
	var items []adfNode
	for item := n.FirstChild(); item != nil; item = item.NextSibling() {
		if inner := c.convertBlocks(item); len(inner) > 0 {
			items = append(items, adfNode{Type: "listItem", Content: inner})
		}
	}
	if len(items) == 0 {
		return adfNode{}, false
	}
	if n.IsOrdered() {
		list := adfNode{Type: "orderedList", Content: items}
		if n.Start > 1 {
			list.Attrs = map[string]any{"order": n.Start}
		}
		return list, true
	}
	return adfNode{Type: "bulletList", Content: items}, true
}

// isTaskList reports whether every item in the list starts with a GFM task
// checkbox and carries only simple content (paragraphs, plus nested task
// lists). ADF taskItem holds inline content only, so anything richer falls
// back to a regular bulletList (the checkbox renders as "[x] " text there).
func (c *mdConverter) isTaskList(n *ast.List) bool {
	if n.FirstChild() == nil {
		return false
	}
	for item := n.FirstChild(); item != nil; item = item.NextSibling() {
		if taskCheckbox(item) == nil {
			return false
		}
		for child := item.FirstChild(); child != nil; child = child.NextSibling() {
			switch child := child.(type) {
			case *ast.Paragraph, *ast.TextBlock:
			case *ast.List:
				if !c.isTaskList(child) {
					return false
				}
			default:
				return false
			}
		}
	}
	return true
}

// taskCheckbox returns the leading TaskCheckBox of a list item, if any.
func taskCheckbox(item ast.Node) *extast.TaskCheckBox {
	first := item.FirstChild()
	if first == nil {
		return nil
	}
	switch first.(type) {
	case *ast.Paragraph, *ast.TextBlock:
	default:
		return nil
	}
	cb, _ := first.FirstChild().(*extast.TaskCheckBox)
	return cb
}

func (c *mdConverter) convertTaskList(n *ast.List) (adfNode, bool) {
	c.taskID++
	list := adfNode{Type: "taskList", Attrs: map[string]any{"localId": fmt.Sprintf("task-list-%d", c.taskID)}}
	for item := n.FirstChild(); item != nil; item = item.NextSibling() {
		state := "TODO"
		if taskCheckbox(item).IsChecked {
			state = "DONE"
		}
		var inl []adfNode
		var nested []adfNode
		for child := item.FirstChild(); child != nil; child = child.NextSibling() {
			switch child := child.(type) {
			case *ast.List:
				if sub, subOK := c.convertTaskList(child); subOK {
					nested = append(nested, sub)
				}
			default: // paragraph / text block (isTaskList guaranteed the shape)
				start := child.FirstChild()
				if _, isBox := start.(*extast.TaskCheckBox); isBox {
					start = start.NextSibling()
				}
				inl = append(inl, c.convertInlineSiblings(start, nil)...)
			}
		}
		if len(inl) == 0 {
			continue
		}
		c.taskID++
		list.Content = append(list.Content, adfNode{
			Type:    "taskItem",
			Attrs:   map[string]any{"localId": fmt.Sprintf("task-item-%d", c.taskID), "state": state},
			Content: inl,
		})
		list.Content = append(list.Content, nested...)
	}
	if len(list.Content) == 0 {
		return adfNode{}, false
	}
	return list, true
}

func (c *mdConverter) convertTable(n *extast.Table) (adfNode, bool) {
	var rows []adfNode
	for row := n.FirstChild(); row != nil; row = row.NextSibling() {
		_, isHeader := row.(*extast.TableHeader)
		cellType := "tableCell"
		if isHeader {
			cellType = "tableHeader"
		}
		var cells []adfNode
		for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
			// ADF table cells require at least one block; an empty
			// paragraph is valid and renders as an empty cell.
			para := adfNode{Type: "paragraph"}
			if inl := c.convertInlines(cell, nil); len(inl) > 0 {
				para.Content = inl
			}
			cells = append(cells, adfNode{Type: cellType, Content: []adfNode{para}})
		}
		if len(cells) > 0 {
			rows = append(rows, adfNode{Type: "tableRow", Content: cells})
		}
	}
	if len(rows) == 0 {
		return adfNode{}, false
	}
	return adfNode{Type: "table", Content: rows}, true
}

// --- inline conversion ---

func (c *mdConverter) convertInlines(parent ast.Node, marks []adfMark) []adfNode {
	return c.convertInlineSiblings(parent.FirstChild(), marks)
}

// convertInlineSiblings converts first and all its following siblings.
// Starting mid-chain lets convertTaskList skip a leading task checkbox.
func (c *mdConverter) convertInlineSiblings(first ast.Node, marks []adfMark) []adfNode {
	return mergeTextNodes(c.rawInlineSiblings(first, marks))
}

func (c *mdConverter) rawInlineSiblings(first ast.Node, marks []adfMark) []adfNode {
	var out []adfNode
	for n := first; n != nil; n = n.NextSibling() {
		switch n := n.(type) {
		case *ast.Text:
			if s := string(n.Segment.Value(c.source)); s != "" {
				out = append(out, markedText(s, marks))
			}
			if n.HardLineBreak() {
				out = append(out, adfNode{Type: "hardBreak"})
			} else if n.SoftLineBreak() {
				out = append(out, markedText(" ", marks))
			}
		case *ast.String:
			if s := string(n.Value); s != "" {
				out = append(out, markedText(s, marks))
			}
		case *ast.CodeSpan:
			if s := c.inlineText(n); s != "" {
				// ADF only allows code to combine with link, so drop
				// any other inherited marks.
				codeMarks := []adfMark{{Type: "code"}}
				for _, m := range marks {
					if m.Type == "link" {
						codeMarks = append(codeMarks, m)
					}
				}
				out = append(out, adfNode{Type: "text", Text: s, Marks: codeMarks})
			}
		case *ast.Emphasis:
			mark := "em"
			if n.Level >= 2 {
				mark = "strong"
			}
			out = append(out, c.convertInlines(n, appendMark(marks, adfMark{Type: mark}))...)
		case *extast.Strikethrough:
			out = append(out, c.convertInlines(n, appendMark(marks, adfMark{Type: "strike"}))...)
		case *ast.Link:
			linked := appendMark(marks, linkMark(string(n.Destination)))
			if inl := c.convertInlines(n, linked); len(inl) > 0 {
				out = append(out, inl...)
			} else if dest := string(n.Destination); dest != "" {
				out = append(out, markedText(dest, linked))
			}
		case *ast.AutoLink:
			url := string(n.URL(c.source))
			label := string(n.Label(c.source))
			if label == "" {
				label = url
			}
			if label != "" {
				out = append(out, markedText(label, appendMark(marks, linkMark(url))))
			}
		case *ast.Image:
			// ADF media requires uploaded attachments; degrade to a link.
			label := c.inlineText(n)
			dest := string(n.Destination)
			if label == "" {
				label = dest
			}
			if label != "" {
				out = append(out, markedText(label, appendMark(marks, linkMark(dest))))
			}
		case *ast.RawHTML:
			var b strings.Builder
			for i := 0; i < n.Segments.Len(); i++ {
				seg := n.Segments.At(i)
				b.Write(seg.Value(c.source))
			}
			if s := b.String(); s != "" {
				out = append(out, markedText(s, marks))
			}
		case *extast.TaskCheckBox:
			// Only reached in the bulletList fallback (convertTaskList
			// skips the checkbox node); render it as text so the state
			// is not lost.
			box := "[ ] "
			if n.IsChecked {
				box = "[x] "
			}
			out = append(out, markedText(box, marks))
		default:
			out = append(out, c.convertInlines(n, marks)...)
		}
	}
	return out
}

// inlineText flattens a node's inline children to plain text. Newlines are
// collapsed so multi-line code spans stay a single ADF text node.
func (c *mdConverter) inlineText(parent ast.Node) string {
	var b strings.Builder
	for n := parent.FirstChild(); n != nil; n = n.NextSibling() {
		switch n := n.(type) {
		case *ast.Text:
			b.Write(n.Segment.Value(c.source))
			if n.SoftLineBreak() || n.HardLineBreak() {
				b.WriteString(" ")
			}
		case *ast.String:
			b.Write(n.Value)
		default:
			b.WriteString(c.inlineText(n))
		}
	}
	return strings.ReplaceAll(b.String(), "\n", " ")
}

// mergeTextNodes coalesces adjacent text nodes carrying identical marks.
// goldmark splits prose into many small Text nodes (linkify scans word by
// word); Jira renders them the same either way, but merged output is far
// smaller and easier to read.
func mergeTextNodes(nodes []adfNode) []adfNode {
	var out []adfNode
	for _, n := range nodes {
		if n.Type == "text" && len(out) > 0 {
			last := &out[len(out)-1]
			if last.Type == "text" && reflect.DeepEqual(last.Marks, n.Marks) {
				last.Text += n.Text
				continue
			}
		}
		out = append(out, n)
	}
	return out
}

func markedText(s string, marks []adfMark) adfNode {
	n := adfNode{Type: "text", Text: s}
	if len(marks) > 0 {
		n.Marks = append([]adfMark(nil), marks...)
	}
	return n
}

func linkMark(href string) adfMark {
	return adfMark{Type: "link", Attrs: map[string]any{"href": href}}
}

// appendMark adds a mark unless one of the same type is already present
// (nested emphasis must not produce duplicate marks — ADF rejects them).
func appendMark(marks []adfMark, m adfMark) []adfMark {
	for _, existing := range marks {
		if existing.Type == m.Type {
			return marks
		}
	}
	out := make([]adfMark, 0, len(marks)+1)
	out = append(out, marks...)
	return append(out, m)
}
