package execution

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Transcript renders the provider's stream-json events into an organized,
// human-readable markdown document as the run progresses. It is fed the same
// raw stdout lines the CliRunner already scans; non-JSON lines and event types
// it does not understand are ignored, so it is safe for any provider that
// emits Claude/Cursor-style stream events.
//
// The output is append-only markdown: each assistant message, thinking block,
// tool call, and tool result becomes its own section, so the file can be
// tailed or opened in any markdown viewer while the agent is still running.
type Transcript struct {
	mu sync.Mutex
	w  io.Writer
}

// TranscriptMeta describes the run for the transcript header.
type TranscriptMeta struct {
	Title    string // task title, e.g. "ERP-42 — Fix login"
	Agent    string
	Model    string
	Step     string
	Instance string
	Started  time.Time
}

// NewTranscript wraps w and writes the document header. w is not closed by
// Transcript; the caller owns its lifecycle.
func NewTranscript(w io.Writer, meta TranscriptMeta) *Transcript {
	t := &Transcript{w: w}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", orDefault(meta.Title, "Agent session"))
	var facts []string
	if meta.Step != "" {
		facts = append(facts, "**Step:** "+meta.Step)
	}
	if meta.Agent != "" {
		facts = append(facts, "**Agent:** "+meta.Agent)
	}
	if meta.Model != "" {
		facts = append(facts, "**Model:** "+meta.Model)
	}
	if meta.Instance != "" {
		facts = append(facts, "**Instance:** `"+meta.Instance+"`")
	}
	if len(facts) > 0 {
		fmt.Fprintf(&b, "> %s\n", strings.Join(facts, " · "))
	}
	if !meta.Started.IsZero() {
		fmt.Fprintf(&b, "> **Started:** %s\n", meta.Started.Format("2006-01-02 15:04:05 MST"))
	}
	b.WriteString("\n---\n")
	t.write(b.String())
	return t
}

// transcriptEvent mirrors the stream-json fields the transcript cares about.
// It is deliberately separate from streamEvent: the transcript needs full
// (untruncated) content plus thinking blocks and tool ids.
type transcriptEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Model   string `json:"model"`
	Result  string `json:"result"`
	Message struct {
		Content []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			Thinking  string          `json:"thinking"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			Content   json.RawMessage `json:"content"`
			ToolUseID string          `json:"tool_use_id"`
			IsError   bool            `json:"is_error"`
		} `json:"content"`
	} `json:"message"`
	DurationMs   int64   `json:"duration_ms"`
	NumTurns     int     `json:"num_turns"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	IsError      bool    `json:"is_error"`
}

// Feed consumes one raw stdout line. Safe for concurrent use.
func (t *Transcript) Feed(line string) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "{") {
		return
	}
	var ev transcriptEvent
	if err := json.Unmarshal([]byte(trimmed), &ev); err != nil || ev.Type == "" {
		return
	}
	switch ev.Type {
	case "system":
		if ev.Subtype == "init" || ev.Model != "" {
			t.write(fmt.Sprintf("\n### 🟢 Session started%s\n", suffixIf(ev.Model, " — `"+ev.Model+"`")))
		}
	case "assistant":
		t.feedAssistant(ev)
	case "user":
		t.feedToolResults(ev)
	case "result", "completion":
		t.feedResult(ev)
	}
}

func (t *Transcript) feedAssistant(ev transcriptEvent) {
	var b strings.Builder
	for _, c := range ev.Message.Content {
		switch c.Type {
		case "thinking":
			if s := strings.TrimSpace(c.Thinking); s != "" {
				b.WriteString("\n### 🧠 Thinking\n\n")
				b.WriteString(blockquote(s))
				b.WriteString("\n")
			}
		case "text":
			if s := strings.TrimSpace(c.Text); s != "" {
				b.WriteString("\n### 💬 Assistant\n\n")
				b.WriteString(s)
				b.WriteString("\n")
			}
		case "tool_use":
			fmt.Fprintf(&b, "\n### 🔧 Tool: `%s`\n\n", c.Name)
			if in := prettyJSON(c.Input); in != "" {
				b.WriteString(fence(in, "json"))
			}
		}
	}
	if b.Len() > 0 {
		t.write(b.String())
	}
}

// toolResultLimit caps how much of a tool result is embedded in the
// transcript; full results still live in the raw task log.
const toolResultLimit = 2000

func (t *Transcript) feedToolResults(ev transcriptEvent) {
	var b strings.Builder
	for _, c := range ev.Message.Content {
		if c.Type != "tool_result" {
			continue
		}
		body := strings.TrimSpace(toolResultText(c.Content))
		if body == "" {
			continue
		}
		truncated := false
		if len(body) > toolResultLimit {
			body = body[:toolResultLimit]
			truncated = true
		}
		label := "Tool result"
		if c.IsError {
			label = "Tool result (error)"
		}
		if truncated {
			label += " — truncated"
		}
		fmt.Fprintf(&b, "\n<details><summary>↩️ %s</summary>\n\n%s\n</details>\n", label, fence(body, ""))
	}
	if b.Len() > 0 {
		t.write(b.String())
	}
}

func (t *Transcript) feedResult(ev transcriptEvent) {
	status := "✅ success"
	if ev.IsError || ev.Subtype == "error" {
		status = "❌ error"
	} else if ev.Subtype != "" && ev.Subtype != "success" {
		status = "⚠️ " + ev.Subtype
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n---\n\n## %s", status)
	if ev.NumTurns > 0 {
		fmt.Fprintf(&b, " · %d turns", ev.NumTurns)
	}
	if ev.DurationMs > 0 {
		fmt.Fprintf(&b, " · %s", (time.Duration(ev.DurationMs) * time.Millisecond).Round(time.Second))
	}
	if ev.TotalCostUSD > 0 {
		fmt.Fprintf(&b, " · $%.4f", ev.TotalCostUSD)
	}
	b.WriteString("\n")
	if r := strings.TrimSpace(ev.Result); r != "" {
		b.WriteString("\n")
		b.WriteString(r)
		b.WriteString("\n")
	}
	t.write(b.String())
}

// jwtPattern matches JWT-shaped bearer tokens (three base64url segments
// starting with "eyJ" — the {"alg"/{"typ" JSON header). Agents routinely
// export tokens in Bash tool calls; keeping them out of transcripts avoids
// spreading credentials into files meant to be read and shared.
var jwtPattern = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)

func (t *Transcript) write(s string) {
	s = jwtPattern.ReplaceAllString(s, "«redacted-jwt»")
	t.mu.Lock()
	defer t.mu.Unlock()
	io.WriteString(t.w, s)
	if f, ok := t.w.(interface{ Sync() error }); ok {
		f.Sync()
	}
}

// toolResultText extracts the human text from a tool_result content payload,
// which the CLIs emit either as a plain string or as an array of
// {type:"text",text:"..."} blocks.
func toolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		for _, blk := range blocks {
			if blk.Type == "text" && strings.TrimSpace(blk.Text) != "" {
				parts = append(parts, blk.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return string(raw)
}

func prettyJSON(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" || s == "{}" {
		return ""
	}
	var buf strings.Builder
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return s
	}
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return s
	}
	return strings.TrimRight(buf.String(), "\n")
}

// fence wraps body in a code fence long enough to survive backticks inside it.
func fence(body, lang string) string {
	marker := "```"
	for strings.Contains(body, marker) {
		marker += "`"
	}
	return marker + lang + "\n" + body + "\n" + marker + "\n"
}

func blockquote(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func suffixIf(cond, s string) string {
	if cond == "" {
		return ""
	}
	return s
}
