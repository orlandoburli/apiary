package editor

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/orlandoburli/apiary/internal/config"
)

// UnsupportedWarning describes a YAML construct the editor cannot fully round-trip.
type UnsupportedWarning struct {
	Location string // e.g. "step 'classify'" or "workflow header"
	Detail   string // human-readable description
}

func (u UnsupportedWarning) String() string {
	return fmt.Sprintf("%s: %s", u.Location, u.Detail)
}

var reAnchor = regexp.MustCompile(`&\S+`)
var reAlias = regexp.MustCompile(`\*\S+`)

// detectUnsupported scans the raw YAML block for a workflow and returns any
// constructs that the visual editor cannot represent or safely round-trip.
// A non-empty return does not block editing — the editor switches affected
// steps to read-only and preserves the original text for those sections.
func detectUnsupported(rawWorkflowBlock string) []UnsupportedWarning {
	var warnings []UnsupportedWarning
	lines := strings.Split(rawWorkflowBlock, "\n")
	for i, line := range lines {
		loc := fmt.Sprintf("line %d", i+1)
		if reAnchor.MatchString(line) {
			warnings = append(warnings, UnsupportedWarning{
				Location: loc,
				Detail:   "YAML anchor (&…) — preserved read-only",
			})
		}
		if reAlias.MatchString(line) {
			warnings = append(warnings, UnsupportedWarning{
				Location: loc,
				Detail:   "YAML alias (*…) — preserved read-only",
			})
		}
	}
	return warnings
}

// unsupportedStepIndices returns the set of step indices (into steps) whose
// raw YAML lines contain anchors (&…) or aliases (*…). It parses the raw
// workflow block to find each step's line range, then checks each range.
func unsupportedStepIndices(rawBlock string, steps []config.StepConfig) map[int]bool {
	result := make(map[int]bool)
	if len(steps) == 0 || rawBlock == "" {
		return result
	}

	lines := strings.Split(rawBlock, "\n")

	// Locate the "steps:" key so we know where the step list begins.
	stepsLine := -1
	stepsKeyIndent := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "steps:" {
			stepsLine = i
			stepsKeyIndent = leadingSpaces(line)
			break
		}
	}
	if stepsLine < 0 {
		return result
	}

	// Each step list item starts with "  - id: <stepID>" at stepsKeyIndent+2.
	itemIndent := stepsKeyIndent + 2

	type stepSpan struct {
		idx   int
		start int
		end   int // exclusive
	}
	var spans []stepSpan

	for i := stepsLine + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := leadingSpaces(line)
		trimmed := strings.TrimSpace(line)

		// A new step list item.
		if indent == itemIndent && strings.HasPrefix(trimmed, "- id: ") {
			stepID := strings.TrimPrefix(trimmed, "- id: ")
			stepID = strings.Trim(stepID, `"'`)
			for si, step := range steps {
				if step.ID == stepID {
					if len(spans) > 0 {
						spans[len(spans)-1].end = i
					}
					spans = append(spans, stepSpan{idx: si, start: i})
					break
				}
			}
			continue
		}
		// Leaving the steps block (back to workflow-level indent or higher).
		if indent <= stepsKeyIndent && !strings.HasPrefix(trimmed, "#") {
			break
		}
	}
	if len(spans) > 0 {
		spans[len(spans)-1].end = len(lines)
	}

	for _, sp := range spans {
		for _, line := range lines[sp.start:sp.end] {
			if reAnchor.MatchString(line) || reAlias.MatchString(line) {
				result[sp.idx] = true
				break
			}
		}
	}
	return result
}
