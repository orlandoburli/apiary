package execution

import (
	"encoding/json"
	"strings"

	"github.com/orlandoburli/apiary/internal/model"
)

// Sentinels the workflow engine uses to extract structured output and a handoff
// summary from an agent's raw stdout. Runners strip these from the visible
// Output and surface the parsed values on RunResult.
const (
	apiaryOutputPrefix = "APIARY_OUTPUT:"
	summaryStartMarker = "APIARY_SUMMARY_START"
	summaryEndMarker   = "APIARY_SUMMARY_END"
	publishStartMarker = "APIARY_PUBLISH_BEGIN"
	publishEndMarker   = "APIARY_PUBLISH_END"
)

// extractStructured scans an agent's raw output for the APIARY_OUTPUT: sentinel,
// the APIARY_SUMMARY_START/END block, and the APIARY_PUBLISH_BEGIN/END block. It
// returns the cleaned output (with those lines removed), the parsed structured
// object (nil when absent or unparseable), the summary text (empty when absent),
// and the publish payload (empty when absent). The last valid APIARY_OUTPUT line
// wins; the publish block is taken verbatim between its markers.
func extractStructured(output string) (cleaned string, structured map[string]any, summary, publish string) {
	lines := strings.Split(output, "\n")
	kept := make([]string, 0, len(lines))
	var summaryLines []string
	var publishLines []string
	inSummary := false
	inPublish := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == summaryStartMarker:
			inSummary = true
		case trimmed == summaryEndMarker:
			inSummary = false
		case trimmed == publishStartMarker:
			inPublish = true
		case trimmed == publishEndMarker:
			inPublish = false
		case inSummary:
			summaryLines = append(summaryLines, line)
		case inPublish:
			publishLines = append(publishLines, line)
		case strings.HasPrefix(trimmed, apiaryOutputPrefix):
			jsonPart := strings.TrimSpace(strings.TrimPrefix(trimmed, apiaryOutputPrefix))
			var obj map[string]any
			if err := json.Unmarshal([]byte(jsonPart), &obj); err == nil {
				structured = obj
			}
			// The sentinel line is stripped whether or not it parsed.
		default:
			kept = append(kept, line)
		}
	}

	cleaned = strings.TrimSpace(strings.Join(kept, "\n"))
	summary = strings.TrimSpace(strings.Join(summaryLines, "\n"))
	publish = strings.TrimSpace(strings.Join(publishLines, "\n"))
	return cleaned, structured, summary, publish
}

// applyStructured post-processes a RunResult's Output, moving any structured
// output, summary, and publish payload into their dedicated fields. Safe to call
// for plain runs: when no sentinels are present, Output is unchanged and the new
// fields stay nil/empty.
func applyStructured(result *model.RunResult) {
	cleaned, structured, summary, publish := extractStructured(result.Output)
	result.Output = cleaned
	result.StructuredOutput = structured
	result.Summary = summary
	result.PublishPayload = publish
}

// summaryInstruction returns the text appended to a prompt instructing the agent
// to emit its handoff summary between the recognized markers.
func summaryInstruction(summaryPrompt string) string {
	var b strings.Builder
	b.WriteString("\n---\n")
	b.WriteString(strings.TrimSpace(summaryPrompt))
	b.WriteString("\n\nWrite that summary between these exact markers, on their own lines:\n")
	b.WriteString(summaryStartMarker + "\n")
	b.WriteString("...your summary here...\n")
	b.WriteString(summaryEndMarker + "\n")
	return b.String()
}
