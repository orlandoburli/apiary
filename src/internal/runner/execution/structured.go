package execution

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
)

// Sentinels the workflow engine uses to extract structured output and a handoff
// summary from an agent's raw stdout. Runners strip these from the visible
// Output and surface the parsed values on RunResult.
const (
	apiaryOutputPrefix  = "APIARY_OUTPUT:"
	summaryStartMarker  = "APIARY_SUMMARY_START"
	summaryEndMarker    = "APIARY_SUMMARY_END"
	publishStartMarker  = "APIARY_PUBLISH_BEGIN"
	publishEndMarker    = "APIARY_PUBLISH_END"
	spawnStartMarker    = "APIARY_SPAWN_BEGIN"
	spawnEndMarker      = "APIARY_SPAWN_END"
	memorizeStartMarker = "APIARY_MEMORIZE_BEGIN"
	memorizeEndMarker   = "APIARY_MEMORIZE_END"
)

// extractStructured scans an agent's raw output for the APIARY_OUTPUT: sentinel
// and the APIARY_SUMMARY_START/END, APIARY_PUBLISH_BEGIN/END,
// APIARY_SPAWN_BEGIN/END and APIARY_MEMORIZE_BEGIN/END blocks. It returns the
// cleaned output (with those lines removed), the parsed structured object (nil
// when absent or unparseable), the summary text, the publish payload, and the
// raw spawn and memorize payloads (empty when absent). The last valid
// APIARY_OUTPUT line wins; block payloads are taken verbatim between their
// markers.
//
// Any block may appear more than once. Spawn and memorize come back one payload
// per block, because each block is a JSON document of its own and joining them
// yields invalid JSON (#494). Summary and publish are free text, so their blocks
// are joined as separate paragraphs. Empty blocks are dropped.
func extractStructured(output string) (cleaned string, structured map[string]any, summary, publish string, spawn, memorize []string) {
	lines := strings.Split(output, "\n")
	kept := make([]string, 0, len(lines))
	var summaryBlocks, publishBlocks, spawnBlocks, memorizeBlocks [][]string
	inSummary := false
	inPublish := false
	inSpawn := false
	inMemorize := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == summaryStartMarker:
			inSummary = true
			summaryBlocks = append(summaryBlocks, nil)
		case trimmed == summaryEndMarker:
			inSummary = false
		case trimmed == publishStartMarker:
			inPublish = true
			publishBlocks = append(publishBlocks, nil)
		case trimmed == publishEndMarker:
			inPublish = false
		case trimmed == spawnStartMarker:
			inSpawn = true
			spawnBlocks = append(spawnBlocks, nil)
		case trimmed == spawnEndMarker:
			inSpawn = false
		case trimmed == memorizeStartMarker:
			inMemorize = true
			memorizeBlocks = append(memorizeBlocks, nil)
		case trimmed == memorizeEndMarker:
			inMemorize = false
		case inSummary:
			last := len(summaryBlocks) - 1
			summaryBlocks[last] = append(summaryBlocks[last], line)
		case inPublish:
			last := len(publishBlocks) - 1
			publishBlocks[last] = append(publishBlocks[last], line)
		case inSpawn:
			last := len(spawnBlocks) - 1
			spawnBlocks[last] = append(spawnBlocks[last], line)
		case inMemorize:
			last := len(memorizeBlocks) - 1
			memorizeBlocks[last] = append(memorizeBlocks[last], line)
		default:
			// The APIARY_OUTPUT: sentinel may arrive bare, or wrapped by an
			// agent in markdown — inline backticks, code fences, or list /
			// blockquote prefixes. outputSentinelJSON tolerates all of these.
			if jsonPart, ok := outputSentinelJSON(trimmed); ok {
				var obj map[string]any
				if err := json.Unmarshal([]byte(jsonPart), &obj); err == nil {
					structured = obj
				}
				// The sentinel line is stripped whether or not it parsed.
				continue
			}
			kept = append(kept, line)
		}
	}

	cleaned = strings.TrimSpace(strings.Join(kept, "\n"))
	summary = strings.Join(blockPayloads(summaryBlocks), "\n\n")
	publish = strings.Join(blockPayloads(publishBlocks), "\n\n")
	spawn = blockPayloads(spawnBlocks)
	memorize = blockPayloads(memorizeBlocks)
	return cleaned, structured, summary, publish, spawn, memorize
}

// blockPayloads renders each block's lines as one trimmed payload, dropping
// blocks with nothing in them.
func blockPayloads(blocks [][]string) []string {
	var payloads []string
	for _, lines := range blocks {
		if payload := strings.TrimSpace(strings.Join(lines, "\n")); payload != "" {
			payloads = append(payloads, payload)
		}
	}
	return payloads
}

// outputSentinelJSON reports whether a trimmed line carries an APIARY_OUTPUT:
// sentinel and, when it does, returns the JSON payload that follows it. The
// sentinel is recognized even when an agent wraps it in markdown — inline
// backticks (`APIARY_OUTPUT: {...}`), code-fence markers, or a list/blockquote
// prefix — so legitimate verdicts are not silently dropped. Anything before the
// sentinel must be markdown wrapper noise; a prose line that merely mentions
// the sentinel is left untouched. The JSON is read up to its balanced closing
// brace, which naturally ignores a trailing backtick or fence marker.
func outputSentinelJSON(trimmed string) (string, bool) {
	idx := strings.Index(trimmed, apiaryOutputPrefix)
	if idx < 0 {
		return "", false
	}
	if !isMarkdownWrapper(trimmed[:idx]) {
		return "", false
	}
	rest := strings.TrimSpace(trimmed[idx+len(apiaryOutputPrefix):])
	if payload := balancedJSON(rest); payload != "" {
		return payload, true
	}
	// No balanced object/array present (e.g. an empty or malformed sentinel).
	// Strip any surrounding fence/backtick noise and hand back what remains so
	// the line is still recognized as a sentinel and stripped from the output.
	return strings.TrimSpace(strings.Trim(rest, "`")), true
}

// isMarkdownWrapper reports whether s consists solely of characters an agent
// might place before the sentinel when wrapping it in markdown: backticks,
// fence tildes, list bullets, blockquote markers, and whitespace.
func isMarkdownWrapper(s string) bool {
	for _, r := range s {
		switch r {
		case '`', '~', '-', '*', '+', '>', ' ', '\t':
			// wrapper noise, keep scanning
		default:
			return false
		}
	}
	return true
}

// balancedJSON returns the first balanced {…} or […] span in s, respecting
// quoted strings and escape sequences, or "" when none is present. It lets the
// caller pull a JSON payload out of a line that has trailing markdown (e.g. a
// closing backtick) after the object.
func balancedJSON(s string) string {
	start := strings.IndexAny(s, "{[")
	if start < 0 {
		return ""
	}
	open := s[start]
	close := byte('}')
	if open == '[' {
		close = ']'
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

// applyStructured post-processes a RunResult's Output, moving any structured
// output, summary, publish payload, spawn requests, and memorize requests into
// their dedicated fields. Safe to call for plain runs: when no sentinels are
// present, Output is unchanged and the new fields stay nil/empty.
//
// A malformed APIARY_SPAWN block is all-or-nothing: RunResult.SpawnError is set,
// no request survives, and the engine fails the step. A retry re-emits every
// block, and a request with no explicit key derives its dedup key from a title
// the agent may reword, so spawning the valid blocks first risks the duplicate
// children of #119. A malformed APIARY_MEMORIZE block only ever becomes a
// warning via RunResult.MemorizeError, so the valid blocks are kept.
func applyStructured(result *model.RunResult) {
	cleaned, structured, summary, publish, spawn, memorize := extractStructured(result.Output)
	result.Output = cleaned
	result.StructuredOutput = structured
	result.Summary = summary
	result.PublishPayload = publish
	result.MemorizeRequests, result.MemorizeError = parseRequestBlocks[model.MemorizeRequest]("APIARY_MEMORIZE", memorize)

	reqs, err := parseRequestBlocks[model.SpawnRequest]("APIARY_SPAWN", spawn)
	switch {
	case err != nil:
		result.SpawnError = err
	case len(spawn) == 1 && !strings.HasPrefix(spawn[0], "["):
		result.SpawnRequest = &reqs[0]
	default:
		result.SpawnRequests = reqs
	}
}

// parseRequestBlocks parses each block on its own and concatenates the requests
// in output order. The returned error reports every malformed block, labelled
// with marker; the requests from the valid blocks are returned alongside it, and
// the caller decides whether a partial set is usable.
func parseRequestBlocks[T any](marker string, blocks []string) ([]T, error) {
	var reqs []T
	var errs []error
	for i, block := range blocks {
		parsed, err := parseRequestBlock[T](block)
		switch {
		case err == nil:
			reqs = append(reqs, parsed...)
		case len(blocks) > 1:
			errs = append(errs, fmt.Errorf("%s: block %d of %d: %w", marker, i+1, len(blocks), err))
		default:
			errs = append(errs, fmt.Errorf("%s: %w", marker, err))
		}
	}
	return reqs, errors.Join(errs...)
}

// parseRequestBlock parses one block, which carries a single object or a JSON
// array of them. A leading '[' selects the array form.
func parseRequestBlock[T any](block string) ([]T, error) {
	if strings.HasPrefix(block, "[") {
		var reqs []T
		if err := json.Unmarshal([]byte(block), &reqs); err != nil {
			return nil, fmt.Errorf("invalid JSON array: %w", err)
		}
		return reqs, nil
	}
	var req T
	if err := json.Unmarshal([]byte(block), &req); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	return []T{req}, nil
}

// OutputSchemaInstruction renders the prompt block that teaches an agent how to
// satisfy a step's output_schema: one bare APIARY_OUTPUT: line with the declared
// fields. Without it only agents whose soul file happens to document the sentinel
// ever emit structured output — every other agent answers in prose, the step
// passes with no structured output (on_missing_output=warn default), and every
// workflow condition keyed on those fields silently misroutes. Returns "" for a
// nil schema (plain steps and legacy routes).
func OutputSchemaInstruction(schema *config.OutputSchema) string {
	if schema == nil || len(schema.Properties) == 0 {
		return ""
	}
	required := map[string]bool{}
	for _, f := range schema.Required {
		required[f] = true
	}
	names := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		names = append(names, name)
	}
	// Required fields first, then alphabetical — deterministic output.
	sort.Slice(names, func(i, j int) bool {
		if required[names[i]] != required[names[j]] {
			return required[names[i]]
		}
		return names[i] < names[j]
	})

	var example []string
	var fields []string
	for _, name := range names {
		f := schema.Properties[name]
		example = append(example, fmt.Sprintf("%q: %s", name, exampleValue(f)))
		desc := f.Type
		if len(f.Enum) > 0 {
			desc = fmt.Sprintf("%s, one of: %s", f.Type, strings.Join(f.Enum, " | "))
		}
		if required[name] {
			desc += ", required"
		}
		fields = append(fields, fmt.Sprintf("- %s (%s)", name, desc))
	}

	var b strings.Builder
	b.WriteString("\n---\n")
	b.WriteString("When you finish, report your structured result by writing EXACTLY one line, bare at the start of a line (no code fence, no backticks, no bold):\n")
	fmt.Fprintf(&b, "%s {%s}\n", apiaryOutputPrefix, strings.Join(example, ", "))
	b.WriteString("Fields:\n")
	b.WriteString(strings.Join(fields, "\n"))
	b.WriteString("\n")
	return b.String()
}

// exampleValue renders a JSON placeholder for one schema field, used in the
// APIARY_OUTPUT example line.
func exampleValue(f config.SchemaField) string {
	if len(f.Enum) > 0 {
		return fmt.Sprintf("%q", strings.Join(f.Enum, "|"))
	}
	switch f.Type {
	case "integer", "number":
		return "0"
	case "boolean":
		return "true"
	case "array":
		return "[...]"
	case "object":
		return "{...}"
	default:
		return `"..."`
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
