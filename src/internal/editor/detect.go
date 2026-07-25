package editor

import (
	"fmt"
	"strings"

	"github.com/orlandoburli/apiary/internal/config"
	"gopkg.in/yaml.v3"
)

// scanAnchoredWorkflows parses rawYAML and returns the set of workflow IDs
// whose YAML blocks contain YAML anchors (&) or aliases (*). Workflows in this
// set are shown in read-only mode in the editor because yaml.Marshal cannot
// preserve anchor/alias topology — it silently inlines every alias.
func scanAnchoredWorkflows(rawYAML []byte) map[string]bool {
	var doc yaml.Node
	if err := yaml.Unmarshal(rawYAML, &doc); err != nil || len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0] // mapping node at the document root
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "workflows" {
			seq := root.Content[i+1]
			anchored := make(map[string]bool, len(seq.Content))
			for _, wfNode := range seq.Content {
				id := yamlMapID(wfNode)
				if id != "" && nodeHasAnchorOrAlias(wfNode) {
					anchored[id] = true
				}
			}
			return anchored
		}
	}
	return nil
}

// yamlMapID returns the value of the "id" key in a YAML mapping node.
func yamlMapID(node *yaml.Node) string {
	if node == nil || node.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == "id" {
			return node.Content[i+1].Value
		}
	}
	return ""
}

// nodeHasAnchorOrAlias reports whether node or any of its descendants is an
// alias node or defines an anchor.
func nodeHasAnchorOrAlias(node *yaml.Node) bool {
	if node == nil {
		return false
	}
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return true
	}
	for _, child := range node.Content {
		if nodeHasAnchorOrAlias(child) {
			return true
		}
	}
	return false
}

// replaceWorkflowInText finds the YAML block for wf.ID in text and replaces it
// with a freshly marshalled representation of wf. All surrounding content
// (agents, sources, runners, settings, comments, ${VAR} placeholders) is
// preserved byte-for-byte. Returns text unchanged when wf.ID is not found.
func replaceWorkflowInText(text string, wf config.WorkflowConfig) (string, error) {
	lines := strings.Split(text, "\n")

	// Locate the list-item line that starts the workflow block.
	startLine := findWorkflowLine(lines, wf.ID)
	if startLine < 0 {
		return text, nil // workflow not present; nothing to replace
	}

	indent := leadingSpaces(lines[startLine])

	// Find the end of the block: the next sibling list-item at the same
	// indentation level, or a non-blank line at a shallower indentation.
	endLine := startLine + 1
	for endLine < len(lines) {
		ln := lines[endLine]
		if strings.TrimSpace(ln) == "" {
			endLine++
			continue
		}
		sp := leadingSpaces(ln)
		trimmed := strings.TrimSpace(ln)
		if sp <= indent && strings.HasPrefix(trimmed, "- ") {
			break // next sibling workflow
		}
		if sp < indent {
			break // parent section
		}
		endLine++
	}

	// Marshal just the workflow struct.
	wfYAML, err := yaml.Marshal(wf)
	if err != nil {
		return "", fmt.Errorf("marshalling workflow: %w", err)
	}

	// Convert the flat YAML produced by Marshal into a proper indented list
	// item at the original indentation level.
	prefix := strings.Repeat(" ", indent)
	wfLines := strings.Split(strings.TrimRight(string(wfYAML), "\n"), "\n")
	var block strings.Builder
	for i, wl := range wfLines {
		if i == 0 {
			block.WriteString(prefix + "- " + wl + "\n")
		} else {
			if strings.TrimSpace(wl) == "" {
				block.WriteString("\n")
			} else {
				block.WriteString(prefix + "  " + wl + "\n")
			}
		}
	}

	// Reassemble with the replaced block.
	var out strings.Builder
	for i, ln := range lines {
		switch {
		case i < startLine:
			out.WriteString(ln)
			out.WriteByte('\n')
		case i == startLine:
			out.WriteString(block.String())
		case i < endLine:
			// skip original block lines (already emitted above)
		default:
			out.WriteString(ln)
			if i < len(lines)-1 {
				out.WriteByte('\n')
			}
		}
	}
	return out.String(), nil
}

// findWorkflowLine returns the index of the YAML list-item line for wfID,
// accepting unquoted, double-quoted, and single-quoted id values.
func findWorkflowLine(lines []string, wfID string) int {
	candidates := [3]string{
		"- id: " + wfID,
		`- id: "` + wfID + `"`,
		"- id: '" + wfID + "'",
	}
	for i, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		for _, c := range candidates {
			if trimmed == c {
				return i
			}
		}
	}
	return -1
}

// leadingSpaces returns the number of leading space characters in s.
func leadingSpaces(s string) int {
	for i, r := range s {
		if r != ' ' {
			return i
		}
	}
	return len(s)
}
