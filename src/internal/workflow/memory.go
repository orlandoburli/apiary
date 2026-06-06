// Package workflow implements the workflow-mode execution model: the shared
// memory object that flows between steps, and (in later phases) the engine that
// orchestrates steps within a workflow instance.
package workflow

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/orlandoburli/apiary/internal/model"
)

// DefaultMemoryMaxChars bounds the injected memory document. When exceeded, the
// builder drops the oldest summaries first; the Cell and Step Data sections are
// never truncated.
const DefaultMemoryMaxChars = 4000

// MemoryStep is one completed step's contribution to workflow memory: the
// structured-output fields it declared via memory.write, and an optional
// handoff summary.
type MemoryStep struct {
	StepID      string
	WriteFields []string       // field names from memory.write
	Structured  map[string]any // parsed structured output (may be nil)
	Summary     string         // handoff summary (may be empty)
}

// MemoryBuilder renders the workflow memory document injected into a step's
// prompt. The zero value is usable (MaxChars defaults to DefaultMemoryMaxChars).
type MemoryBuilder struct {
	MaxChars int
}

// Build assembles the memory document from the originating Cell and the ordered
// list of completed step contributions. Later steps override earlier ones when
// they write the same key (last-write-wins).
func (b MemoryBuilder) Build(cell model.SourceItem, steps []MemoryStep) string {
	maxChars := b.MaxChars
	if maxChars <= 0 {
		maxChars = DefaultMemoryMaxChars
	}

	// Step Data: accumulate declared write-fields in first-seen key order with
	// last-write-wins values.
	var keyOrder []string
	values := map[string]string{}
	for _, s := range steps {
		for _, field := range s.WriteFields {
			v, ok := s.Structured[field]
			if !ok {
				continue
			}
			if _, seen := values[field]; !seen {
				keyOrder = append(keyOrder, field)
			}
			values[field] = renderValue(v)
		}
	}

	// Summaries, in step order.
	type summary struct{ stepID, text string }
	var summaries []summary
	for _, s := range steps {
		if strings.TrimSpace(s.Summary) != "" {
			summaries = append(summaries, summary{s.StepID, s.Summary})
		}
	}

	render := func(includeSummaries int) string {
		var b strings.Builder
		b.WriteString("=== Workflow Memory ===\n\n")

		b.WriteString("[Cell]\n")
		writeKV(&b, "title", cell.Title)
		if len(cell.Labels) > 0 {
			writeKV(&b, "labels", strings.Join(cell.Labels, ", "))
		}
		if cell.Type != "" {
			writeKV(&b, "type", cell.Type)
		}
		if cell.Priority != "" {
			writeKV(&b, "priority", cell.Priority)
		}
		if cell.SourceID != "" {
			writeKV(&b, "source", cell.SourceID)
		}

		if len(keyOrder) > 0 {
			b.WriteString("\n[Step Data]\n")
			for _, k := range keyOrder {
				writeKV(&b, k, values[k])
			}
		}

		// includeSummaries < 0 means "all"; otherwise include the last N
		// (newest) summaries — oldest are dropped first under truncation.
		shown := summaries
		if includeSummaries >= 0 && includeSummaries < len(summaries) {
			shown = summaries[len(summaries)-includeSummaries:]
		}
		if len(shown) > 0 {
			b.WriteString("\n[Summaries]\n")
			for _, s := range shown {
				fmt.Fprintf(&b, "%s: |\n", s.stepID)
				for _, line := range strings.Split(strings.TrimRight(s.text, "\n"), "\n") {
					b.WriteString("  " + line + "\n")
				}
			}
		}

		b.WriteString("\n======================")
		return b.String()
	}

	doc := render(-1)
	if len(doc) <= maxChars {
		return doc
	}

	// Over budget: drop oldest summaries one at a time until it fits.
	for keep := len(summaries) - 1; keep >= 0; keep-- {
		doc = render(keep)
		if len(doc) <= maxChars {
			return doc
		}
	}

	// Even with no summaries it's still over budget (very large Step Data). The
	// Cell and Step Data sections are never truncated, so return the
	// summary-free document whole and accept exceeding the soft limit.
	return render(0)
}

// writeKV writes an aligned "key: value" line.
func writeKV(b *strings.Builder, key, value string) {
	fmt.Fprintf(b, "%s: %s\n", key, value)
}

// renderValue renders a structured-output value for the memory document.
// Strings pass through; scalars are formatted; arrays/objects become compact JSON.
func renderValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		return fmt.Sprintf("%t", x)
	case float64:
		// JSON numbers decode as float64; render integers without a decimal point.
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%g", x)
	case nil:
		return ""
	default:
		if data, err := json.Marshal(x); err == nil {
			return string(data)
		}
		return fmt.Sprintf("%v", x)
	}
}
