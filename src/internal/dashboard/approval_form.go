package dashboard

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/orlandoburli/apiary/internal/db"
)

// The approval sheet (`a`, `y`, or `n` on a parked request).
//
// An approval step can ask for more than yes/no: it declares the outcomes it
// accepts, and typed `fields` — string, text, boolean, number, choice — whose
// answers reach the workflow as memory.<field>. The dashboard used to post a
// bare decision and drop those fields, which left a choice gate unanswerable from
// the TUI and sent operators to the signed webhook for what is a local action.
//
// It also never recorded a reason. A rejection that explained itself lived only
// in the operator's head, so the next person to look at the gate could not tell
// why it stopped. Every answer now carries an optional reason, and y/n open this
// sheet instead of firing immediately so there is always somewhere to type it.
//
// One overlay renders the context, the step's question, the declared outcomes as
// a radio list, the fields, and the reason, then posts all of it in a single
// response.

const (
	// Which part of the sheet owns the cursor. The outcome radio stays visible
	// (with ●) while the cursor is elsewhere so the pending answer is never lost.
	approvalFocusOutcome = iota
	approvalFocusFields
	approvalFocusReason
)

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
// no fields yields none, and the sheet degrades to its outcome list.
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

// approvalOutcomes returns the answers the gate accepts. An empty declaration
// means the classic pair; synthesizing it here keeps every caller — rendering,
// key handling, submission — on one shape.
func approvalOutcomes(req *db.ApprovalRequest) []db.ApprovalOutcome {
	if req != nil && len(req.Outcomes) > 0 {
		out := make([]db.ApprovalOutcome, 0, len(req.Outcomes))
		for _, o := range req.Outcomes {
			if o.Value != "" {
				out = append(out, o)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return []db.ApprovalOutcome{
		{Value: "approve"},
		{Value: "reject", Fail: true},
	}
}

// selectApprovalOutcome finds the row y/n should land on.
//
// A workflow names its outcomes freely ("ship", "hold"), so a literal match on
// approve/reject is tried first and then the nearest one with the matching Fail
// flag — y lands on an outcome that keeps the run going, n on one that stops it.
func selectApprovalOutcome(outcomes []db.ApprovalOutcome, preselect string) int {
	if len(outcomes) == 0 {
		return 0
	}
	if preselect == "" {
		return 0
	}
	for i, o := range outcomes {
		if strings.EqualFold(o.Value, preselect) {
			return i
		}
	}
	wantFail := preselect == "reject"
	for i, o := range outcomes {
		if o.Fail == wantFail {
			return i
		}
	}
	return 0
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

// openApprovalForm points the sheet at a pending request, seeding every field
// with its zero value so a submission without edits still sends well-typed
// values. It selects the gate's first outcome.
func (a *App) openApprovalForm(req *db.ApprovalRequest) {
	a.openApprovalFormWith(req, "")
}

// openApprovalFormWith opens the sheet with one outcome preselected. An empty
// preselect means the first, which is what `a` (review before answering) wants.
func (a *App) openApprovalFormWith(req *db.ApprovalRequest, preselect string) {
	a.model.approvalActive = true
	a.model.approvalReq = req
	a.model.approvalErr = ""
	a.model.approvalVals = map[string]any{}
	a.model.approvalDraft = map[string]string{}
	a.model.approvalReason = ""
	a.model.approvalMsg = renderApprovalMessage(req.Message)
	a.model.approvalOutcomes = approvalOutcomes(req)
	a.model.approvalOutcome = selectApprovalOutcome(a.model.approvalOutcomes, preselect)
	a.model.approvalCursor = a.model.approvalOutcome
	a.model.approvalFocus = approvalFocusOutcome
	a.model.approvalIdx = 0
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

// answerApproval opens the sheet for a parked instance.
//
// y and n used to answer in one keystroke, which made a reason impossible to
// give; they now only choose which outcome starts selected. `a` reviews the gate
// from its first outcome without expressing a preference.
func (a *App) answerApproval(req *db.ApprovalRequest, key string) (tea.Model, tea.Cmd) {
	if req == nil {
		return a, nil
	}
	switch key {
	case "y":
		a.openApprovalFormWith(req, "approve")
	case "n":
		a.openApprovalFormWith(req, "reject")
	default:
		a.openApprovalForm(req)
	}
	return a, nil
}

func (a *App) closeApprovalForm() {
	a.model.approvalActive = false
	a.model.approvalReq = nil
	a.model.approvalVals = nil
	a.model.approvalDraft = nil
	a.model.approvalOutcomes = nil
	a.model.approvalOutcome = 0
	a.model.approvalCursor = 0
	a.model.approvalFocus = approvalFocusOutcome
	a.model.approvalReason = ""
	a.model.approvalErr = ""
	a.model.approvalIdx = 0
	a.model.approvalMsg = nil
}

// handleApprovalFormKey owns every key while the sheet is open.
//
// The outcome radio, the typed fields, and the reason all take printable
// characters, so the decision keys cannot be letters: ctrl+s submits from
// anywhere, esc cancels. Enter selects the outcome under the cursor (a radio
// group is a choice, not a commit) and submits from every other row — the footer
// says so, since the same key meaning two things needs stating.
func (a *App) handleApprovalFormKey(key string) (tea.Model, tea.Cmd) {
	req := a.model.approvalReq
	if req == nil {
		a.closeApprovalForm()
		return a, nil
	}
	fields := approvalFields(req)
	outcomes := a.model.approvalOutcomes
	if a.model.approvalIdx >= len(fields) {
		a.model.approvalIdx = max(0, len(fields)-1)
	}
	if a.model.approvalOutcome >= len(outcomes) {
		a.model.approvalOutcome = max(0, len(outcomes)-1)
	}
	if a.model.approvalCursor >= len(outcomes) {
		a.model.approvalCursor = max(0, len(outcomes)-1)
	}

	switch key {
	case "esc":
		a.closeApprovalForm()
		return a, nil

	case "ctrl+s":
		return a.submitApproval()

	case "up":
		a.moveApprovalFocus(-1, fields)
		return a, nil

	case "down":
		a.moveApprovalFocus(1, fields)
		return a, nil

	case "tab":
		a.cycleApprovalFocus(1, fields)
		return a, nil

	case "shift+tab":
		a.cycleApprovalFocus(-1, fields)
		return a, nil

	case "left", "right":
		// On the outcome list the arrows move the selected answer; inside a
		// choice or boolean field they keep their old editing meaning.
		if a.model.approvalFocus == approvalFocusOutcome {
			a.moveApprovalOutcome(key)
			return a, nil
		}

	case "enter", " ":
		if a.model.approvalFocus == approvalFocusOutcome {
			// Confirming a radio row must not answer the gate: the operator is
			// one keystroke from deciding and needs to reach the reason first.
			a.model.approvalOutcome = a.model.approvalCursor
			return a, nil
		}
		if key == " " && a.model.approvalFocus == approvalFocusReason {
			a.editApprovalReason(key)
			return a, nil
		}
		if key == "enter" {
			return a.submitApproval()
		}
	}

	switch a.model.approvalFocus {
	case approvalFocusOutcome:
		// 1-9 jump straight to an answer. Other letters are swallowed rather
		// than leaking to the dashboard's global bindings.
		if n, err := strconv.Atoi(key); err == nil && n >= 1 && n <= len(outcomes) {
			a.model.approvalOutcome = n - 1
			a.model.approvalCursor = n - 1
		}
		return a, nil

	case approvalFocusReason:
		a.editApprovalReason(key)
		return a, nil
	}

	if len(fields) == 0 {
		return a, nil
	}
	a.model.approvalErr = ""
	a.editApprovalField(fields[a.model.approvalIdx], key)
	return a, nil
}

// moveApprovalFocus walks up or down through the sheet's rows. Within a section
// it moves the row cursor; at the edges it crosses into the neighbouring section
// so the whole sheet is reachable with the arrows alone.
func (a *App) moveApprovalFocus(delta int, fields []approvalField) {
	switch a.model.approvalFocus {
	case approvalFocusOutcome:
		if delta > 0 {
			if a.model.approvalCursor < len(a.model.approvalOutcomes)-1 {
				a.model.approvalCursor++
				return
			}
			if len(fields) > 0 {
				a.model.approvalFocus = approvalFocusFields
				a.model.approvalIdx = 0
				return
			}
			a.model.approvalFocus = approvalFocusReason
		}

	case approvalFocusFields:
		if delta > 0 {
			if a.model.approvalIdx < len(fields)-1 {
				a.model.approvalIdx++
			} else {
				a.model.approvalFocus = approvalFocusReason
			}
			return
		}
		if a.model.approvalIdx > 0 {
			a.model.approvalIdx--
		} else {
			a.model.approvalFocus = approvalFocusOutcome
			a.model.approvalCursor = a.model.approvalOutcome
		}

	case approvalFocusReason:
		if delta < 0 {
			if len(fields) > 0 {
				a.model.approvalFocus = approvalFocusFields
				a.model.approvalIdx = len(fields) - 1
			} else {
				a.model.approvalFocus = approvalFocusOutcome
				a.model.approvalCursor = a.model.approvalOutcome
			}
		}
	}
}

// cycleApprovalFocus moves between sections with tab, skipping the field section
// when the gate declares none.
func (a *App) cycleApprovalFocus(delta int, fields []approvalField) {
	sections := []int{approvalFocusOutcome}
	if len(fields) > 0 {
		sections = append(sections, approvalFocusFields)
	}
	sections = append(sections, approvalFocusReason)

	pos := 0
	for i, s := range sections {
		if s == a.model.approvalFocus {
			pos = i
			break
		}
	}
	pos = (pos + delta + len(sections)) % len(sections)

	switch sections[pos] {
	case approvalFocusOutcome:
		a.model.approvalCursor = a.model.approvalOutcome
	case approvalFocusFields:
		if a.model.approvalIdx >= len(fields) {
			a.model.approvalIdx = max(0, len(fields)-1)
		}
	}
	a.model.approvalFocus = sections[pos]
}

// moveApprovalOutcome changes the selected answer with ←/→. The cursor follows so
// the ▶ marker and the ● radio never disagree.
func (a *App) moveApprovalOutcome(key string) {
	n := len(a.model.approvalOutcomes)
	if n == 0 {
		return
	}
	cur := a.model.approvalOutcome
	if key == "left" {
		cur = (cur - 1 + n) % n
	} else {
		cur = (cur + 1) % n
	}
	a.model.approvalOutcome = cur
	a.model.approvalCursor = cur
}

// editApprovalReason appends printable runes and edits the free-text reason. It
// is always available and never required.
func (a *App) editApprovalReason(key string) {
	switch key {
	case "backspace":
		if s := a.model.approvalReason; s != "" {
			a.model.approvalReason = s[:len(s)-1]
		}
	case "ctrl+u":
		a.model.approvalReason = ""
	default:
		// Single printable runes only; named keys like "pgup" are not text.
		if len([]rune(key)) == 1 {
			a.model.approvalReason += key
		}
	}
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

// submitApproval turns the sheet into a response. The outcome decides the
// decision — Fail means the workflow is told "reject" — and a failing answer
// skips field validation entirely, the same way a refusal always has: refusing a
// gate must never mean filling in its form.
func (a *App) submitApproval() (tea.Model, tea.Cmd) {
	req := a.model.approvalReq
	if req == nil {
		a.closeApprovalForm()
		return a, nil
	}
	outcomes := a.model.approvalOutcomes
	if len(outcomes) == 0 {
		outcomes = approvalOutcomes(req)
	}
	idx := a.model.approvalOutcome
	if idx < 0 || idx >= len(outcomes) {
		idx = 0
	}
	selected := outcomes[idx]

	decision := "approve"
	if selected.Fail {
		decision = "reject"
	}

	var values map[string]any
	if !selected.Fail {
		v, err := a.collectApprovalValues(approvalFields(req))
		if err != nil {
			a.model.approvalErr = err.Error()
			return a, nil
		}
		values = v
	}

	feedback := strings.TrimSpace(a.model.approvalReason)
	id := req.ID
	a.closeApprovalForm()
	return a, a.approvalResponseCmd(id, decision, selected.Value, values, feedback)
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

// renderApprovalForm overlays the sheet on the current view, mirroring the
// workflow picker's dialog treatment.
//
// The message is the only flexible section: everything below it is how the gate
// gets answered, so the message is what gets cut when the terminal is short.
func (a *App) renderApprovalForm(view string) string {
	req := a.model.approvalReq
	if req == nil {
		return view
	}
	fields := approvalFields(req)
	outcomes := a.model.approvalOutcomes

	head := []string{
		StyleBoxTitle.Render(" Answer approval "),
		StyleFooterDim.Render(" " + approvalContextLine(req)),
	}
	if len(req.Approvers) > 0 {
		quorum := req.RequiredApprovals
		if quorum < 1 {
			quorum = 1
		}
		head = append(head, StyleFooterDim.Render(fmt.Sprintf(
			" approvers: %s (%d required)", strings.Join(req.Approvers, ", "), quorum)))
	}

	tail := make([]string, 0, len(fields)*2+len(outcomes)+12)
	tail = append(tail, "", StyleLabel.Render(" Outcomes"))
	for i, o := range outcomes {
		tail = append(tail, a.renderApprovalOutcome(o, i))
	}

	if len(fields) > 0 {
		tail = append(tail, "", StyleLabel.Render(" Fields"))
		for i, f := range fields {
			cursor := "  "
			if a.model.approvalFocus == approvalFocusFields && i == a.model.approvalIdx {
				cursor = StyleAccent.Render("▸ ")
			}
			label := f.Label
			if f.Required {
				label += "*"
			}
			tail = append(tail, cursor+StyleLabel.Render(label))
			tail = append(tail, "    "+a.renderApprovalValue(
				f, a.model.approvalFocus == approvalFocusFields && i == a.model.approvalIdx))
		}
	}

	tail = append(tail,
		"",
		StyleLabel.Render(" Reason (optional)"),
		" "+a.renderApprovalReason(),
	)
	if a.model.approvalErr != "" {
		tail = append(tail, "", StyleError.Render("  "+a.model.approvalErr))
	}
	tail = append(tail, "", approvalKeyHints())

	headHeight := lipgloss.Height(strings.Join(head, "\n"))
	tailHeight := lipgloss.Height(strings.Join(tail, "\n"))
	// Border top+bottom and the box's two padding rows live outside the text
	// column; the blank separator above the message is part of the content. The
	// message takes whatever is left.
	budget := a.model.height - 5 - headHeight - tailHeight
	message := a.approvalMessageLines(budget)

	rows := append([]string{}, head...)
	if len(message) > 0 {
		rows = append(rows, "")
		rows = append(rows, message...)
	}
	rows = append(rows, tail...)

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorWarning).
		Padding(1, 3).
		Width(approvalDialogWidth).
		Render(lipgloss.JoinVertical(lipgloss.Left, rows...))

	topPad := (a.model.height - lipgloss.Height(dialog)) / 2
	if topPad < 0 {
		topPad = 0
	}
	return strings.Repeat("\n", topPad) +
		lipgloss.NewStyle().Width(a.model.width).Align(lipgloss.Center).Render(dialog)
}

// approvalContextLine names what is being answered: the request, the step, and
// the ticket or pull request when the daemon stitched one on.
func approvalContextLine(req *db.ApprovalRequest) string {
	parts := []string{req.ID}
	if req.StepID != "" {
		parts = append(parts, "step "+req.StepID)
	}
	if req.TicketRef != "" {
		parts = append(parts, "ticket "+req.TicketRef)
	}
	if req.PRNumber > 0 {
		parts = append(parts, fmt.Sprintf("PR #%d", req.PRNumber))
	}
	return ansi.Truncate(strings.Join(parts, " · "), approvalInnerWidth, "…")
}

// renderApprovalOutcome draws one radio row. ▶ is where the cursor is, ● is what
// will be submitted; they differ until the operator confirms a row with enter.
func (a *App) renderApprovalOutcome(o db.ApprovalOutcome, idx int) string {
	cursor := "  "
	if a.model.approvalFocus == approvalFocusOutcome && idx == a.model.approvalCursor {
		cursor = StyleAccent.Render("▶ ")
	}
	radio := StyleMuted.Render("○ ")
	if idx == a.model.approvalOutcome {
		radio = StyleAccent.Render("● ")
	}
	label := o.Label
	if label == "" {
		label = o.Value
	}
	style := StyleMuted
	if idx == a.model.approvalOutcome {
		style = StyleLabel
	}
	return " " + cursor + radio + StyleFooterDim.Render(fmt.Sprintf("%d ", idx+1)) + style.Render(label)
}

// renderApprovalReason draws the free-text row, with a caret while it has focus.
func (a *App) renderApprovalReason() string {
	text := a.model.approvalReason
	if a.model.approvalFocus == approvalFocusReason {
		return text + "▏"
	}
	if text == "" {
		return StyleMuted.Render("—")
	}
	return text
}

// approvalKeyHints states the new key map, including the one key with two
// meanings: enter confirms a radio row but submits from a field or the reason.
func approvalKeyHints() string {
	key := StyleFooterKey.Render
	dim := StyleFooterDim.Render
	return dim("↑↓ move · tab section · 1-9 pick · ←→ outcome") + "\n" +
		dim("⏎ on an outcome: select only") + "\n" +
		key("⏎") + dim(" on a field or the reason: submit · ") +
		key("^s") + dim(" submit · ") +
		key("esc") + dim(" cancel")
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

const (
	approvalDialogWidth = 68
	// The box is 68 wide with 3 columns of padding on each side, leaving 62 for
	// text; keeping the constant in step with the dialog avoids re-wrapping.
	approvalInnerWidth = approvalDialogWidth - 6
)

// renderApprovalMessage turns the request's message into display lines.
//
// The message is authored in the workflow config and routinely carries markdown
// — lists of what is about to ship, links, inline code, fenced diffs. It used to
// be squashed into a single yellow paragraph by a word-wrapper that split on
// whitespace, which threw away every line break, list and code block: the
// operator answering the gate saw less than the author wrote. It is rendered as
// markdown instead, once when the sheet opens (glamour is far too slow for the
// render path, which runs on every keystroke), and falls back to a plain wrap
// when glamour fails so a message is never dropped.
func renderApprovalMessage(msg string) []string {
	return markdownLines(msg, approvalInnerWidth)
}

// markdownLines renders markdown to display lines wrapped to width, falling back
// to a plain wrap when glamour fails so text is never dropped.
func markdownLines(src string, width int) []string {
	src = strings.TrimSpace(src)
	if src == "" {
		return nil
	}
	if width < 8 {
		width = 8
	}
	rendered, err := renderMarkdown(src, width)
	if err != nil {
		return clampToWidth(wrapPlain(src, width), width)
	}
	return clampToWidth(strings.Split(strings.Trim(rendered, "\n"), "\n"), width)
}

// approvalMessageLines returns the rendered message trimmed to what the terminal
// can show: a long message must not push the outcomes, fields and key hints off
// the screen, since those are how the gate gets answered. What does not fit is
// replaced by a count, and the full text stays available in
// `apiary approvals <request-id>`.
func (a *App) approvalMessageLines(budget int) []string {
	lines := a.model.approvalMsg
	if len(lines) == 0 {
		return nil
	}
	if a.model.height <= 0 || len(lines) <= budget {
		return lines
	}
	if budget < 1 {
		return nil
	}
	kept := append([]string{}, lines[:budget-1]...)
	id := "<request-id>"
	if a.model.approvalReq != nil {
		id = a.model.approvalReq.ID
	}
	return append(kept, StyleFooterDim.Render(fmt.Sprintf(
		"… %d more lines — apiary approvals %s", len(lines)-(budget-1), id)))
}
