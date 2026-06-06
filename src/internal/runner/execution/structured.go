package execution

import (
	"encoding/json"
	"fmt"
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
	spawnStartMarker   = "APIARY_SPAWN_BEGIN"
	spawnEndMarker     = "APIARY_SPAWN_END"
)

// extractStructured scans an agent's raw output for the APIARY_OUTPUT: sentinel,
// the APIARY_SUMMARY_START/END block, the APIARY_PUBLISH_BEGIN/END block, and the
// APIARY_SPAWN_BEGIN/END block. It returns the cleaned output (with those lines
// removed), the parsed structured object (nil when absent or unparseable), the
// summary text, the publish payload, and the raw spawn payload (all empty when
// absent). The last valid APIARY_OUTPUT line wins; block payloads are taken
// verbatim between their markers.
func extractStructured(output string) (cleaned string, structured map[string]any, summary, publish, spawn string) {
	lines := strings.Split(output, "\n")
	kept := make([]string, 0, len(lines))
	var summaryLines []string
	var publishLines []string
	var spawnLines []string
	inSummary := false
	inPublish := false
	inSpawn := false

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
		case trimmed == spawnStartMarker:
			inSpawn = true
		case trimmed == spawnEndMarker:
			inSpawn = false
		case inSummary:
			summaryLines = append(summaryLines, line)
		case inPublish:
			publishLines = append(publishLines, line)
		case inSpawn:
			spawnLines = append(spawnLines, line)
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
	spawn = strings.TrimSpace(strings.Join(spawnLines, "\n"))
	return cleaned, structured, summary, publish, spawn
}

// applyStructured post-processes a RunResult's Output, moving any structured
// output, summary, publish payload, and spawn request into their dedicated
// fields. Safe to call for plain runs: when no sentinels are present, Output is
// unchanged and the new fields stay nil/empty. A malformed APIARY_SPAWN block
// (invalid JSON) is surfaced via RunResult.SpawnError so the engine fails the step.
func applyStructured(result *model.RunResult) {
	cleaned, structured, summary, publish, spawn := extractStructured(result.Output)
	result.Output = cleaned
	result.StructuredOutput = structured
	result.Summary = summary
	result.PublishPayload = publish
	if spawn != "" {
		var req model.SpawnRequest
		if err := json.Unmarshal([]byte(spawn), &req); err != nil {
			result.SpawnError = fmt.Errorf("APIARY_SPAWN: invalid JSON: %w", err)
		} else {
			result.SpawnRequest = &req
		}
	}
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
