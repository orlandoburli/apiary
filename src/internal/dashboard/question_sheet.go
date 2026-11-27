package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/orlandoburli/apiary/internal/db"
)

// Questions an agent asks mid-run through the ask_human tool.
//
// A question blocks the agent, so the dashboard polls for them on its ordinary
// tick and opens the oldest on its own: nobody should have to remember to go
// looking for the thing that is holding up a run. The sheet reuses the approval
// sheet's shell — same title, markdown body, radio rows, text rows and footer —
// because answering either is the same kind of interruption.
//
// Auto-open yields to any overlay that owns the keyboard. A question is not so
// urgent that it may yank a half-typed workflow picker or approval out from under
// the operator; while one of those is open the footer carries a badge and `?`
// opens the queue on demand.

const (
	questionFocusOptions = iota
	questionFocusAnswer
	questionFocusNote
)

// questionsMsg carries the pending queue back from the tick poll.
type questionsMsg struct {
	questions []db.AgentQuestion
}

// questionAnsweredMsg reports the result of POSTing an answer. gone marks a 409:
// someone else answered, or the question expired, so it must leave the queue
// rather than be retried forever.
type questionAnsweredMsg struct {
	id   string
	err  string
	gone bool
}

// fetchQuestions loads the pending queue. One indexed query per tick, on every
// tab: the point of the poll is to notice a blocked agent before the operator
// happens to open Tasks, so scoping it to a tab would defeat it.
func (a *App) fetchQuestions() tea.Cmd {
	dbConn := a.dbConn
	return func() tea.Msg {
		if dbConn == nil {
			return questionsMsg{}
		}
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		qs, err := dbConn.ListPendingQuestions(ctx, 50)
		if err != nil {
			// Polling is ambient; a locked DB must not paint an error banner every
			// two seconds. The next tick retries, and an explicit answer reports
			// its own failure.
			return questionsMsg{}
		}
		return questionsMsg{questions: liveQuestions(qs, time.Now())}
	}
}

// liveQuestions drops rows whose deadline has passed. The store's sweep is lazy
// (it runs on long-poll reads), so a pending row can briefly outlive its own
// expiry; letting that reach the sheet would offer an answer that cannot stick.
func liveQuestions(qs []db.AgentQuestion, now time.Time) []db.AgentQuestion {
	out := make([]db.AgentQuestion, 0, len(qs))
	for _, q := range qs {
		if q.ExpiresAt != nil && !now.Before(*q.ExpiresAt) {
			continue
		}
		out = append(out, q)
	}
	return out
}

// overlayOwnsKeys reports whether a modal or a text field is already using the
// keyboard. Auto-open checks it so a question never interrupts an answer in
// progress; the footer badge is the alternative while this is true.
func (a *App) overlayOwnsKeys() bool {
	if a.model.approvalActive || a.model.pickerActive || a.model.confirmAction != "" || a.model.questionActive {
		return true
	}
	return a.model.tasksTab != nil && a.model.tasksTab.FilterActive
}

// handleQuestionsMsg stores the poll result and opens the oldest question when
// nothing else is using the screen.
func (a *App) handleQuestionsMsg(msg questionsMsg) (tea.Model, tea.Cmd) {
	a.model.pendingQuestions = msg.questions
	a.pruneQuestionSnoozes(msg.questions)

	if a.model.questionActive {
		// An answer from another channel or the expiry sweep removes the row; the
		// sheet must go with it rather than keep offering a dead question.
		if a.model.question == nil || !questionInQueue(msg.questions, a.model.question.ID) {
			a.closeQuestionSheet()
			a.openOldestQuestion()
		}
		return a, nil
	}
	a.openOldestQuestion()
	return a, nil
}

// openOldestQuestion shows the queue's oldest pending question, if any and if no
// overlay owns the keyboard. The store lists newest-first, so the oldest is last.
func (a *App) openOldestQuestion() {
	if a.overlayOwnsKeys() || len(a.model.pendingQuestions) == 0 {
		return
	}
	// Newest first, so walking from the end reaches the question that has
	// waited longest. A snoozed one is skipped: the operator already said
	// "later", and re-opening it every poll is not what later means.
	qs := a.model.pendingQuestions
	for i := len(qs) - 1; i >= 0; i-- {
		if a.model.questionSnoozed[qs[i].ID] {
			continue
		}
		a.openQuestion(qs[i])
		return
	}
}

// openNextQuestion cycles the queue. `?` uses it when the sheet is already open;
// otherwise the oldest is the one that has waited longest.
func (a *App) openNextQuestion() {
	qs := a.model.pendingQuestions
	if len(qs) == 0 {
		return
	}
	next := len(qs) - 1
	if a.model.questionActive && a.model.question != nil {
		for i := range qs {
			if qs[i].ID == a.model.question.ID {
				next = (i + 1) % len(qs)
				break
			}
		}
	}
	// `?` is an explicit request, so it overrides an earlier "later".
	delete(a.model.questionSnoozed, qs[next].ID)
	a.openQuestion(qs[next])
}

// pruneQuestionSnoozes drops ids that are no longer pending, so answering or
// expiring a question does not leave a permanent hole in the map.
func (a *App) pruneQuestionSnoozes(qs []db.AgentQuestion) {
	if len(a.model.questionSnoozed) == 0 {
		return
	}
	live := make(map[string]bool, len(qs))
	for _, q := range qs {
		live[q.ID] = true
	}
	for id := range a.model.questionSnoozed {
		if !live[id] {
			delete(a.model.questionSnoozed, id)
		}
	}
}

func questionInQueue(qs []db.AgentQuestion, id string) bool {
	for _, q := range qs {
		if q.ID == id {
			return true
		}
	}
	return false
}

// openQuestion points the sheet at one question and seeds its answer. An option
// is chosen up front when one exists: it is the common answer, and it matches
// the daemon's rule that a gate without free text must answer with an option.
func (a *App) openQuestion(q db.AgentQuestion) {
	qcopy := q
	a.model.questionActive = true
	a.model.question = &qcopy
	a.model.questionErr = ""
	a.model.questionMsg = renderQuestionMessage(q.Question)
	a.model.questionAnswer = ""
	a.model.questionNote = ""
	a.model.questionOption = -1
	a.model.questionCursor = 0
	if len(q.Options) > 0 {
		a.selectQuestionOption(0)
		a.model.questionFocus = questionFocusOptions
		return
	}
	if q.AllowFreeText {
		a.model.questionFocus = questionFocusAnswer
		return
	}
	a.model.questionFocus = questionFocusNote
}

func (a *App) closeQuestionSheet() {
	a.model.questionActive = false
	a.model.question = nil
	a.model.questionMsg = nil
	a.model.questionFocus = questionFocusOptions
	a.model.questionCursor = 0
	a.model.questionOption = -1
	a.model.questionAnswer = ""
	a.model.questionNote = ""
	a.model.questionErr = ""
}

// handleQuestionSheetKey owns every key while the sheet is open. Enter submits
// from anywhere (the footer says so); esc leaves the question pending, which is
// why it is back on the next tick — the point is to postpone, not to discard.
func (a *App) handleQuestionSheetKey(key string) (tea.Model, tea.Cmd) {
	q := a.model.question
	if q == nil {
		a.closeQuestionSheet()
		return a, nil
	}

	switch key {
	case "esc":
		a.model.questionSnoozed[a.model.question.ID] = true
		a.closeQuestionSheet()
		a.setNotice("Question left for later — press ? to answer it", false)
		return a, nil
	case "?":
		a.openNextQuestion()
		return a, nil
	case "enter":
		return a, a.submitQuestion()
	case "up":
		a.moveQuestionFocus(-1)
		return a, nil
	case "down":
		a.moveQuestionFocus(1)
		return a, nil
	case "tab":
		a.cycleQuestionFocus(1)
		return a, nil
	case "shift+tab":
		a.cycleQuestionFocus(-1)
		return a, nil
	case "left", "right":
		if a.model.questionFocus == questionFocusOptions {
			a.moveQuestionOption(key)
		}
		return a, nil
	}

	switch a.model.questionFocus {
	case questionFocusOptions:
		// 1-9 pick an option directly. Other letters are swallowed so a stray
		// global shortcut cannot fire underneath the sheet.
		if n, err := strconv.Atoi(key); err == nil && n >= 1 && n <= len(q.Options) {
			a.selectQuestionOption(n - 1)
		}
		return a, nil

	case questionFocusAnswer:
		if !q.AllowFreeText {
			return a, nil
		}
		before := a.model.questionAnswer
		a.editQuestionText(&a.model.questionAnswer, key)
		if a.model.questionAnswer != before {
			// Typed text is now the answer; the radio no longer describes what
			// will be submitted until an option is chosen again.
			a.model.questionOption = -1
		}
		return a, nil

	case questionFocusNote:
		a.editQuestionText(&a.model.questionNote, key)
		return a, nil
	}
	return a, nil
}

// moveQuestionFocus walks the rows. On the option list the cursor is the choice,
// so moving it fills the answer; past either end it crosses into the next
// section, making the whole sheet reachable with the arrows alone.
func (a *App) moveQuestionFocus(delta int) {
	q := a.model.question
	if q == nil {
		return
	}
	switch a.model.questionFocus {
	case questionFocusOptions:
		if delta > 0 {
			if a.model.questionCursor < len(q.Options)-1 {
				a.selectQuestionOption(a.model.questionCursor + 1)
				return
			}
			if q.AllowFreeText {
				a.prepareQuestionAnswer()
			} else {
				a.model.questionFocus = questionFocusNote
			}
			return
		}
		if a.model.questionCursor > 0 {
			a.selectQuestionOption(a.model.questionCursor - 1)
		}

	case questionFocusAnswer:
		if delta > 0 {
			a.model.questionFocus = questionFocusNote
			return
		}
		if len(q.Options) > 0 {
			a.model.questionFocus = questionFocusOptions
		}

	case questionFocusNote:
		if delta < 0 {
			if q.AllowFreeText {
				a.prepareQuestionAnswer()
			} else if len(q.Options) > 0 {
				a.model.questionFocus = questionFocusOptions
			}
		}
	}
}

// cycleQuestionFocus tabs between the sections that are actually available.
func (a *App) cycleQuestionFocus(delta int) {
	q := a.model.question
	if q == nil {
		return
	}
	sections := []int{}
	if len(q.Options) > 0 {
		sections = append(sections, questionFocusOptions)
	}
	if q.AllowFreeText {
		sections = append(sections, questionFocusAnswer)
	}
	sections = append(sections, questionFocusNote)

	pos := 0
	for i, s := range sections {
		if s == a.model.questionFocus {
			pos = i
			break
		}
	}
	pos = (pos + delta + len(sections)) % len(sections)
	if sections[pos] == questionFocusAnswer {
		a.prepareQuestionAnswer()
		return
	}
	a.model.questionFocus = sections[pos]
}

// prepareQuestionAnswer clears an option-filled answer when the operator reaches
// the free-text row. Without that, the first keystroke appends to the option's
// text and "typing overrides the choice" becomes an edit of it.
func (a *App) prepareQuestionAnswer() {
	a.model.questionFocus = questionFocusAnswer
	if a.model.questionOption >= 0 {
		a.model.questionAnswer = ""
		a.model.questionOption = -1
	}
}

// selectQuestionOption makes an option the answer. The radio and the Answer row
// are one value: picking an option fills the text, and typing over it later wins.
func (a *App) selectQuestionOption(idx int) {
	q := a.model.question
	if q == nil || idx < 0 || idx >= len(q.Options) {
		return
	}
	a.model.questionOption = idx
	a.model.questionCursor = idx
	a.model.questionAnswer = q.Options[idx].Value
}

// moveQuestionOption steps the radio with ←/→, wrapping at the ends.
func (a *App) moveQuestionOption(key string) {
	q := a.model.question
	if q == nil || len(q.Options) == 0 {
		return
	}
	cur := a.model.questionOption
	if cur < 0 {
		cur = 0
		if key == "left" {
			cur = len(q.Options) - 1
		}
	} else if key == "left" {
		cur = (cur - 1 + len(q.Options)) % len(q.Options)
	} else {
		cur = (cur + 1) % len(q.Options)
	}
	a.selectQuestionOption(cur)
}

// editQuestionText applies one keystroke to a free-text row.
func (a *App) editQuestionText(dst *string, key string) {
	switch key {
	case "backspace":
		if s := *dst; s != "" {
			*dst = s[:len(s)-1]
		}
	case "ctrl+u":
		*dst = ""
	default:
		if len([]rune(key)) == 1 {
			*dst += key
		}
	}
}

// submitQuestion posts the answer and note. The sheet stays open until the
// daemon confirms: a failure must leave the question answerable, not vanish it.
func (a *App) submitQuestion() tea.Cmd {
	q := a.model.question
	if q == nil {
		return nil
	}
	answer := strings.TrimSpace(a.model.questionAnswer)
	if answer == "" && a.model.questionOption >= 0 && a.model.questionOption < len(q.Options) {
		answer = q.Options[a.model.questionOption].Value
	}
	return a.answerQuestionCmd(q.ID, answer, strings.TrimSpace(a.model.questionNote))
}

// answerQuestionCmd posts an answer over the dashboard's Unix-socket IPC. It is
// deliberately the same shape as approvalResponseCmd so the daemon's error text
// reaches the operator unchanged.
func (a *App) answerQuestionCmd(id, answer, reason string) tea.Cmd {
	socketPath := a.socketPath
	return func() tea.Msg {
		payload := map[string]string{
			"answer": answer,
			"reason": reason,
			"actor":  approvalActor(),
		}
		body, _ := json.Marshal(payload)
		transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		}}
		client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
		req, _ := http.NewRequest(http.MethodPost, "http://apiary/questions/"+id+"/answer", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return questionAnsweredMsg{id: id, err: fmt.Sprintf("Answer failed: cannot reach daemon: %v", err)}
		}
		defer resp.Body.Close()
		if msg, ok := ipcFailure(resp, "Answer"); !ok {
			// 409 means the row is answered or expired: it must leave the queue.
			return questionAnsweredMsg{id: id, err: msg.text, gone: resp.StatusCode == http.StatusConflict}
		}
		return questionAnsweredMsg{id: id}
	}
}

// handleQuestionAnswered removes a confirmed answer, reports a conflict as gone,
// and keeps any other failure on screen so it can be retried.
func (a *App) handleQuestionAnswered(msg questionAnsweredMsg) (tea.Model, tea.Cmd) {
	if msg.err != "" && !msg.gone {
		if a.model.questionActive {
			a.model.questionErr = msg.err
		} else {
			a.setNotice(msg.err, true)
		}
		return a, nil
	}

	a.model.pendingQuestions = removeQuestion(a.model.pendingQuestions, msg.id)
	if msg.gone {
		a.closeQuestionSheet()
		a.setNotice(msg.err, true)
		a.openOldestQuestion()
		return a, nil
	}

	a.closeQuestionSheet()
	a.setNotice("Question answered — the agent is unblocked", false)
	a.openOldestQuestion()
	return a, nil
}

func removeQuestion(qs []db.AgentQuestion, id string) []db.AgentQuestion {
	out := qs[:0]
	for _, q := range qs {
		if q.ID != id {
			out = append(out, q)
		}
	}
	return out
}

func (a *App) setNotice(text string, isErr bool) {
	a.model.notice = text
	a.model.noticeIsErr = isErr
	a.model.noticeUntil = time.Now().Add(noticeTTL)
}

// renderQuestionSheet overlays the sheet on the current view.
//
// The message is the only flexible section; the options, answer and note are how
// the question gets answered, so a long question is what gets cut on a short
// terminal.
func (a *App) renderQuestionSheet(view string) string {
	q := a.model.question
	if q == nil {
		return view
	}

	head := []string{
		StyleBoxTitle.Render(" Answer question "),
		StyleFooterDim.Render(" " + questionContextLine(q)),
	}

	tail := make([]string, 0, len(q.Options)+12)
	if len(q.Options) > 0 {
		tail = append(tail, "", StyleLabel.Render(" Options"))
		for i, o := range q.Options {
			tail = append(tail, a.renderQuestionOption(o, i))
		}
	}

	tail = append(tail, "", StyleLabel.Render(" Answer"))
	if q.AllowFreeText {
		tail = append(tail, " "+a.renderQuestionAnswer())
	} else {
		tail = append(tail, " "+StyleMuted.Render("options only"))
	}

	tail = append(tail,
		"",
		StyleLabel.Render(" Note (optional)"),
		" "+a.renderQuestionNote(),
	)
	if a.model.questionErr != "" {
		tail = append(tail, "", StyleError.Render("  "+a.model.questionErr))
	}
	tail = append(tail, "", questionKeyHints())

	headHeight := lipgloss.Height(strings.Join(head, "\n"))
	tailHeight := lipgloss.Height(strings.Join(tail, "\n"))
	// Border top+bottom and the two padding rows live outside the text column;
	// the blank separator above the message is still content.
	budget := a.model.height - 5 - headHeight - tailHeight
	message := a.questionMessageLines(budget)

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

// questionContextLine names who is blocked and for how long.
func questionContextLine(q *db.AgentQuestion) string {
	parts := make([]string, 0, 4)
	if q.AgentID != "" {
		parts = append(parts, "agent "+q.AgentID)
	}
	if q.StepID != "" {
		parts = append(parts, "step "+q.StepID)
	}
	if q.TaskID != "" {
		parts = append(parts, "task "+q.TaskID)
	}
	if age := waitingAge(time.Since(q.CreatedAt)); age != "—" {
		parts = append(parts, age+" ago")
	}
	line := strings.Join(parts, " · ")
	if line == "" {
		line = q.ID
	}
	return ansi.Truncate(line, approvalInnerWidth, "…")
}

// renderQuestionOption draws one radio row. ▶ is the focus, ● is the chosen
// answer; on the option list they travel together.
func (a *App) renderQuestionOption(o db.QuestionOption, idx int) string {
	cursor := "  "
	if a.model.questionFocus == questionFocusOptions && idx == a.model.questionCursor {
		cursor = StyleAccent.Render("▶ ")
	}
	radio := StyleMuted.Render("○ ")
	if idx == a.model.questionOption {
		radio = StyleAccent.Render("● ")
	}
	label := o.Label
	if label == "" {
		label = o.Value
	}
	style := StyleMuted
	if idx == a.model.questionOption {
		style = StyleLabel
	}
	return " " + cursor + radio + StyleFooterDim.Render(fmt.Sprintf("%d ", idx+1)) + style.Render(label)
}

func (a *App) renderQuestionAnswer() string {
	if !a.model.question.AllowFreeText {
		return ""
	}
	text := a.model.questionAnswer
	if a.model.questionFocus == questionFocusAnswer {
		return text + "▏"
	}
	if text == "" {
		return StyleMuted.Render("—")
	}
	return text
}

func (a *App) renderQuestionNote() string {
	text := a.model.questionNote
	if a.model.questionFocus == questionFocusNote {
		return text + "▏"
	}
	if text == "" {
		return StyleMuted.Render("—")
	}
	return text
}

// questionKeyHints states the sheet's two important facts: enter submits, and esc
// postpones rather than discards, so the question returns on the next poll.
func questionKeyHints() string {
	key := StyleFooterKey.Render
	dim := StyleFooterDim.Render
	return dim("↑↓ move · tab section · 1-9 pick · ←→ choose") + "\n" +
		key("⏎") + dim(" submit · ") + key("esc") + dim(" later — stays pending, press ? to reopen")
}

// renderQuestionMessage renders the agent's question once, when the sheet opens;
// glamour is far too slow for the per-keystroke render path.
func renderQuestionMessage(msg string) []string {
	return markdownLines(msg, approvalInnerWidth)
}

// questionMessageLines trims the rendered question to what the terminal can show,
// mirroring the approval sheet: truncation beats pushing the controls off screen.
func (a *App) questionMessageLines(budget int) []string {
	lines := a.model.questionMsg
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
	return append(kept, StyleFooterDim.Render(fmt.Sprintf(
		"… %d more lines", len(lines)-(budget-1))))
}
