package editor

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/orlandoburli/apiary/internal/config"
)

// WorkflowToYAML marshals a single WorkflowConfig to a YAML string.
// The output starts with "id: ..." (no leading "- ") — suitable for display
// in the YAML preview pane or as input to ReplaceWorkflowInRaw.
func WorkflowToYAML(wf config.WorkflowConfig) (string, error) {
	data, err := yaml.Marshal(wf)
	if err != nil {
		return "", fmt.Errorf("marshalling workflow: %w", err)
	}
	return string(data), nil
}

// ReplaceWorkflowInRaw finds the workflow block identified by workflowID in
// rawYAML and replaces it with the marshaled representation of wf. All other
// content in rawYAML (other workflows, other config sections, comments outside
// the replaced block) is preserved byte-for-byte.
//
// If the workflow block is not found, an error is returned — the caller should
// fall back to a full yaml.Marshal of the entire Config.
func ReplaceWorkflowInRaw(rawYAML, workflowID string, wf config.WorkflowConfig) (string, error) {
	newBlock, err := WorkflowToYAML(wf)
	if err != nil {
		return "", err
	}

	lines := strings.Split(rawYAML, "\n")

	// Find the start line: "  - id: <workflowID>" (2-space indent, list item).
	startLine := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "- id: "+workflowID {
			startLine = i
			break
		}
	}
	if startLine < 0 {
		return "", fmt.Errorf("workflow %q not found in raw YAML", workflowID)
	}

	// Determine the indent of the list item (the "- " prefix).
	itemIndent := leadingSpaces(lines[startLine])

	// Find the end line: the next line at the same or lower indent level that
	// starts a new list item ("- "), or end of file.
	endLine := len(lines)
	for i := startLine + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := leadingSpaces(line)
		trimmed := strings.TrimSpace(line)
		if indent <= itemIndent && strings.HasPrefix(trimmed, "- ") {
			endLine = i
			break
		}
		// Also stop at a section boundary (a non-indented key).
		if indent < itemIndent && !strings.HasPrefix(trimmed, "#") {
			endLine = i
			break
		}
	}

	// Build the replacement block: indent the marshaled YAML to match the
	// original list item indentation.
	prefix := strings.Repeat(" ", itemIndent)
	replacement := indentWorkflowBlock(newBlock, prefix)

	// Splice: lines[:startLine] + replacement + lines[endLine:]
	var out strings.Builder
	for _, l := range lines[:startLine] {
		out.WriteString(l + "\n")
	}
	out.WriteString(replacement)
	// replacement already ends with "\n"; don't double it.
	for i, l := range lines[endLine:] {
		if i > 0 || strings.TrimSpace(l) != "" {
			out.WriteString(l + "\n")
		} else {
			out.WriteString(l + "\n")
		}
	}

	result := out.String()
	// Trim trailing newlines beyond one.
	result = strings.TrimRight(result, "\n") + "\n"
	return result, nil
}

// indentWorkflowBlock takes a marshaled WorkflowConfig YAML (lines starting
// at "id: ...") and formats it as a YAML list item with the given indentation.
//
// Input:
//
//	id: my-workflow
//	steps:
//	  - id: step1
//
// Output (prefix = "  "):
//
//	  - id: my-workflow
//	    steps:
//	      - id: step1
func indentWorkflowBlock(yamlBlock, prefix string) string {
	lines := strings.Split(strings.TrimRight(yamlBlock, "\n"), "\n")
	if len(lines) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, line := range lines {
		if i == 0 {
			// First line: "- " marker
			sb.WriteString(prefix + "- " + line + "\n")
		} else {
			if strings.TrimSpace(line) == "" {
				sb.WriteString("\n")
			} else {
				// Continuation: additional 2 spaces to align with the "- " marker
				sb.WriteString(prefix + "  " + line + "\n")
			}
		}
	}
	return sb.String()
}

// ExtractWorkflowBlock returns the raw YAML text for the workflow block with
// the given ID. Useful for computing the diff between original and edited.
func ExtractWorkflowBlock(rawYAML, workflowID string) string {
	lines := strings.Split(rawYAML, "\n")

	startLine := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "- id: "+workflowID {
			startLine = i
			break
		}
	}
	if startLine < 0 {
		return ""
	}

	itemIndent := leadingSpaces(lines[startLine])
	endLine := len(lines)
	for i := startLine + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := leadingSpaces(line)
		trimmed := strings.TrimSpace(line)
		if indent <= itemIndent && strings.HasPrefix(trimmed, "- ") {
			endLine = i
			break
		}
		if indent < itemIndent && !strings.HasPrefix(trimmed, "#") {
			endLine = i
			break
		}
	}

	return strings.Join(lines[startLine:endLine], "\n")
}

func leadingSpaces(s string) int {
	return len(s) - len(strings.TrimLeft(s, " \t"))
}
