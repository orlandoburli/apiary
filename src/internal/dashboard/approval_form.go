package dashboard

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/orlandoburli/apiary/internal/db"
)

// The approval form (`a`, or `y`/`n` on a request that declares fields).
//
// An approval step can ask for more than yes/no: it declares typed `fields` —
// string, text, boolean, number, choice — whose answers reach the workflow as
// memory.<field>, so later steps can branch on them. The dashboard used to post a
// bare decision and drop those fields, which left a choice gate unanswerable from
// the TUI and sent operators to the signed webhook for what is a local action.
//
// The form renders one row per field, collects the values, and posts them with
// the decision in a single response.

// approvalField is one editable row of the form, normalized out of the request's
// JSON field descriptors (which arrive as map[string]any from the store).
type approvalField struct {
	Name     string
	Label    string
	Type     string
	Required bool
	Options  []string
}

// approvalFields normalizes a request's declared fields into rows. A request with
// no fields yields none, and the form degrades to a plain approve/reject prompt.
func approvalFields(req *db.ApprovalRequest) []approvalField {
	if req == nil {
		return nil
	}
	out := make([]approvalField, 0, len(req.Fields))
	for _, raw := range req.Fields {
		name, _ := raw["name"].(string)
		if name == "" {
			continue
		}
		f := approvalField{
			Name:     name,
			Label:    stringOr(raw["label"], name),
			Type:     stringOr(raw["type"], "string"),
			Required: boolOr(raw["required"]),
			Options:  stringSlice(raw["options"]),
		}
		out = append(out, f)
	}
	return out
}

// stringOr reads a string out of a decoded JSON value, falling back when it is
// absent or empty.
func stringOr(v any, fallback string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return fallback
}

func boolOr(v any) bool {
	b, _ := v.(bool)
	return b
}

// stringSlice reads a choice field's options. They arrive as []any through JSON
// and as []string from a config built in memory, so both are accepted.
func stringSlice(v any) []string {
	switch vals := v.(type) {
	case []string:
		return vals
	case []any:
		out := make([]string, 0, len(vals))
		for _, item := range vals {
			if s, ok := item.(string); ok {
				out = append(out, s)
			} else if item != nil {
				out = append(out, fmt.Sprint(item))
			}
		}
		return out
	}
	return nil
}

// openApprovalForm points the form at a pending request and seeds every field with
// its zero value, so a form submitted without edits still sends well-typed values.
func (a *App) openApprovalForm(req *db.ApprovalRequest) {
	a.model.approvalActive = true
	a.model.approvalReq = req
	a.model.approvalIdx = 0
	a.model.approvalErr = ""
	a.model.approvalVals = map[string]any{}
	a.model.approvalDraft = map[string]string{}
	for _, f := range approvalFields(req) {
		switch f.Type {
		case "boolean":
			a.model.approvalVals[f.Name] = false
		case "choice":
			if len(f.Options) > 0 {
				a.model.approvalVals[f.Name] = f.Options[0]
			}
		default:
			a.model.approvalDraft[f.Name] = ""
		}
	}
}

// answerApproval routes an approval keystroke on a parked instance.
//
//   - n rejects outright. A refusal ends the gate, so it never collects fields.
//   - y approves a field-less gate in one key, and otherwise opens the form,
//     because the values are part of the answer and cannot be guessed.
//   - a always opens the form, for reviewing a gate before answering it.
func (a *App) answerApproval(req *db.ApprovalRequest, key string) (tea.Model, tea.Cmd) {
	if req == nil {
		return a, nil
	}
	switch {
	case key == "n":
		return a, a.approvalResponseCmd(req.ID, "reject", nil, "")
	case key == "y" && len(approvalFields(req)) == 0:
		return a, a.approvalResponseCmd(req.ID, "approve", nil, "")
	default:
		a.openApprovalForm(req)
		return a, nil
	}
}

func (a *App) closeApprovalForm() {
	a.model.approvalActive = false
	a.model.approvalReq = nil
	a.model.approvalVals = nil
	a.model.approvalDraft = nil
	a.model.approvalErr = ""
	a.model.approvalIdx = 0
}

// handleApprovalFormKey owns every key while the form is open.
//
// Typed fields consume printable characters, so the decision keys must not be
// letters: enter approves, ctrl+r rejects, esc cancels. A rejection skips field
// collection entirely — refusing a gate should never mean filling in its form.
func (a *App) handleApprovalFormKey(key string) (tea.Model, tea.Cmd) {
	req := a.model.approvalReq
	if req == nil {
		a.closeApprovalForm()
		return a, nil
	}
	fields := approvalFields(req)
	if a.model.approvalIdx >= len(fields) {
		a.model.approvalIdx = max(0, len(fields)-1)
	}

	switch key {
	case "esc":
		a.closeApprovalForm()
		return a, nil

	case "ctrl+r":
		id := req.ID
		a.closeApprovalForm()
		return a, a.approvalResponseCmd(id, "reject", nil, "")

	case "enter":
		values, err := a.collectApprovalValues(fields)
		if err != nil {
			a.model.approvalErr = err.Error()
			return a, nil
		}
		id := req.ID
		a.closeApprovalForm()
		return a, a.approvalResponseCmd(id, "approve", values, "")

	case "up", "shift+tab":
		if a.model.approvalIdx > 0 {
			a.model.approvalIdx--
		}
		return a, nil

	case "down", "tab":
		if a.model.approvalIdx < len(fields)-1 {
			a.model.approvalIdx++
		}
		return a, nil
	}

	if len(fields) == 0 {
		return a, nil
	}
	a.model.approvalErr = ""
	a.editApprovalField(fields[a.model.approvalIdx], key)
	return a, nil
}

// editApprovalField applies one keystroke to the focused row. Choice and boolean
// rows are driven with the arrow keys and space so they never swallow text; every
// other type takes printable characters.
func (a *App) editApprovalField(f approvalField, key string) {
	switch f.Type {
	case "choice":
		if len(f.Options) == 0 {
			return
		}
		cur := 0
		for i, opt := range f.Options {
			if a.model.approvalVals[f.Name] == opt {
				cur = i
			}
		}
		switch key {
		case "left", "h":
			cur = (cur - 1 + len(f.Options)) % len(f.Options)
		case "right", "l", " ":
			cur = (cur + 1) % len(f.Options)
		default:
			// 1-9 jump straight to an option.
			if n, err := strconv.Atoi(key); err == nil && n >= 1 && n <= len(f.Options) {
				cur = n - 1
			} else {
				return
			}
		}
		a.model.approvalVals[f.Name] = f.Options[cur]

	case "boolean":
		switch key {
		case " ", "left", "right", "h", "l", "y", "n":
			cur, _ := a.model.approvalVals[f.Name].(bool)
			if key == "y" {
				cur = false // toggled to true below
			} else if key == "n" {
				a.model.approvalVals[f.Name] = false
				return
			}
			a.model.approvalVals[f.Name] = !cur
		}

	default: // string, text, number
		switch key {
		case "backspace":
			if s := a.model.approvalDraft[f.Name]; s != "" {
				a.model.approvalDraft[f.Name] = s[:len(s)-1]
			}
		case "ctrl+u":
			a.model.approvalDraft[f.Name] = ""
		case " ":
			a.model.approvalDraft[f.Name] += " "
		default:
			// Single printable runes only; ignore named keys like "pgup".
			if len([]rune(key)) == 1 {
				a.model.approvalDraft[f.Name] += key
			}
		}
	}
}

// collectApprovalValues turns the form state into the response payload, applying
// the same rules the daemon enforces in validateApprovalResponse: required fields
// must be present, numbers must parse, choices must be one of the options.
//
// Validating locally is a courtesy — it keeps a typo from becoming a round trip —
// but the daemon remains the authority and its rejection is what the operator
// ultimately sees.
func (a *App) collectApprovalValues(fields []approvalField) (map[string]any, error) {
	values := map[string]any{}
	for _, f := range fields {
		switch f.Type {
		case "boolean":
			values[f.Name] = boolOr(a.model.approvalVals[f.Name])

		case "choice":
			sel, _ := a.model.approvalVals[f.Name].(string)
			if sel == "" {
				if f.Required {
					return nil, fmt.Errorf("%s: choose an option", f.Label)
				}
				continue
			}
			values[f.Name] = sel

		case "number":
			raw := strings.TrimSpace(a.model.approvalDraft[f.Name])
			if raw == "" {
				if f.Required {
					return nil, fmt.Errorf("%s is required", f.Label)
				}
				continue
			}
			n, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return nil, fmt.Errorf("%s must be a number", f.Label)
			}
			values[f.Name] = n

		default:
			raw := strings.TrimSpace(a.model.approvalDraft[f.Name])
			if raw == "" {
				if f.Required {
					return nil, fmt.Errorf("%s is required", f.Label)
				}
				continue
			}
			values[f.Name] = raw
		}
	}
	return values, nil
}

// renderApprovalForm overlays the form on the current view, mirroring the
// workflow picker's dialog treatment.
func (a *App) renderApprovalForm(view string) string {
	req := a.model.approvalReq
	if req == nil {
		return view
	}
	fields := approvalFields(req)

	rows := make([]string, 0, len(fields)+6)
	rows = append(rows, StyleBoxTitle.Render(" Answer approval "))
	if req.Message != "" {
		rows = append(rows, StyleWarning.Render(wrapTo(req.Message, 52)))
	}
	if len(req.Approvers) > 0 {
		quorum := req.RequiredApprovals
		if quorum < 1 {
			quorum = 1
		}
		rows = append(rows, StyleFooterDim.Render(fmt.Sprintf(
			"approvers: %s (%d required)", strings.Join(req.Approvers, ", "), quorum)))
	}
	rows = append(rows, "")

	for i, f := range fields {
		cursor := "  "
		if i == a.model.approvalIdx {
			cursor = StyleAccent.Render("▸ ")
		}
		label := f.Label
		if f.Required {
			label += "*"
		}
		rows = append(rows, cursor+StyleLabel.Render(label))
		rows = append(rows, "    "+a.renderApprovalValue(f, i == a.model.approvalIdx))
	}
	if len(fields) == 0 {
		rows = append(rows, StyleFooterDim.Render("  no fields — approve or reject"))
	}

	rows = append(rows, "")
	if a.model.approvalErr != "" {
		rows = append(rows, StyleError.Render("  "+a.model.approvalErr), "")
	}
	rows = append(rows,
		StyleFooterKey.Render(" ⏎ ")+" "+StyleFooterLbl.Render("approve")+"  "+
			StyleFooterKey.Render(" ^r ")+" "+StyleFooterLbl.Render("reject")+"  "+
			StyleFooterKey.Render(" esc ")+" "+StyleFooterLbl.Render("cancel"))

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorWarning).
		Padding(1, 3).
		Width(58).
		Render(lipgloss.JoinVertical(lipgloss.Left, rows...))

	topPad := (a.model.height - lipgloss.Height(dialog)) / 2
	if topPad < 0 {
		topPad = 0
	}
	return strings.Repeat("\n", topPad) +
		lipgloss.NewStyle().Width(a.model.width).Align(lipgloss.Center).Render(dialog)
}

// renderApprovalValue draws one row's current value: choices as a horizontal
// option strip so every alternative is visible, booleans as a toggle, everything
// else as a text field with a caret when focused.
func (a *App) renderApprovalValue(f approvalField, focused bool) string {
	switch f.Type {
	case "choice":
		sel, _ := a.model.approvalVals[f.Name].(string)
		parts := make([]string, 0, len(f.Options))
		for _, opt := range f.Options {
			if opt == sel {
				parts = append(parts, StyleFooterKey.Render(" "+opt+" "))
			} else {
				parts = append(parts, StyleMuted.Render(" "+opt+" "))
			}
		}
		return strings.Join(parts, " ")

	case "boolean":
		if boolOr(a.model.approvalVals[f.Name]) {
			return StyleFooterKey.Render(" yes ") + " " + StyleMuted.Render(" no ")
		}
		return StyleMuted.Render(" yes ") + " " + StyleFooterKey.Render(" no ")

	default:
		text := a.model.approvalDraft[f.Name]
		if focused {
			text += "▏"
		}
		if text == "" {
			text = StyleMuted.Render("—")
		}
		return text
	}
}

// wrapTo hard-wraps text at width on word boundaries, for the dialog's fixed
// column. lipgloss wraps rendered blocks, but the message is styled per line.
func wrapTo(s string, width int) string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}
	var lines []string
	line := words[0]
	for _, w := range words[1:] {
		if len(line)+1+len(w) > width {
			lines = append(lines, line)
			line = w
			continue
		}
		line += " " + w
	}
	return strings.Join(append(lines, line), "\n")
}
