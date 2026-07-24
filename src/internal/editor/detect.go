package editor

import (
	"fmt"
	"regexp"
	"strings"
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
