// Package editor provides a bidirectional TUI workflow editor backed by YAML.
//
// The editor loads a workflow from an apiary.yaml file, lets the user navigate
// and edit steps visually, and saves changes back to the same file. The
// canonical representation always remains the YAML file on disk; the visual
// editor is an authoring surface.
//
// Key behaviours:
//   - Round-trip editing preserves semantic YAML content; comments and ordering
//     outside the edited workflow block are not disturbed.
//   - Unsupported YAML constructs (anchors, aliases) are detected and presented
//     as read-only; they are never silently discarded on save.
//   - Validation errors from config.Config.Validate() are displayed inline and
//     attached to the relevant step node.
//   - A diff between the original and modified workflow YAML is shown before
//     every save so users can review changes.
package editor

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/orlandoburli/apiary/internal/config"
)

// viewMode is which panel the editor is rendering.
type viewMode int

const (
	viewGraph viewMode = iota // side-by-side graph + detail
	viewYAML                  // full-screen YAML preview
	viewDiff                  // diff before save
	viewDiffConfirm           // diff shown waiting for y/n to save
)

// editTarget is what the cursor is on.
type editTarget int

const (
	targetTrigger editTarget = iota
	targetStep
)

// Model is the Bubble Tea model for the workflow editor.
type Model struct {
	cfg         *config.Config
	cfgPath     string
	workflowIdx int // index into cfg.Workflows

	view viewMode
	form *Form // non-nil when editing a step/trigger

	// Graph navigation
	target     editTarget
	stepIdx    int // index of selected step (valid when target == targetStep)
	stepScroll int // scroll offset for the step list

	// Terminal size
	width, height int

	// State
	dirty       bool   // unsaved changes
	origBlock   string // raw YAML of the workflow block at load time (for diff)
	statusMsg   string // transient status line
	statusIsErr bool

	// Unsupported constructs detected at load time
	unsupported []UnsupportedWarning
	// readOnlySteps maps step index → true for steps that contain YAML anchors
	// or aliases. Such steps cannot be edited through the form interface because
	// yaml.Marshal would silently discard anchor/alias topology on re-marshal.
	readOnlySteps map[int]bool

	// Diff computed when entering viewDiffConfirm
	pendingDiff []DiffLine
	yamlScroll  int // scroll in yaml/diff views
}

// New creates an editor model for the workflow at workflowIdx in cfg.
// rawYAML is the original file content (for round-trip preservation).
func New(cfg *config.Config, cfgPath string, workflowIdx int, rawYAML string) *Model {
	wf := cfg.Workflows[workflowIdx]
	origBlock := ExtractWorkflowBlock(rawYAML, wf.ID)
	warnings := detectUnsupported(origBlock)

	return &Model{
		cfg:           cfg,
		cfgPath:       cfgPath,
		workflowIdx:   workflowIdx,
		view:          viewGraph,
		target:        targetTrigger,
		origBlock:     origBlock,
		unsupported:   warnings,
		readOnlySteps: buildReadOnlySteps(origBlock, wf.Steps),
	}
}

// buildReadOnlySteps returns a map from step index → true for every step whose
// raw YAML sub-block contains a YAML anchor (&) or alias (*). Steps in the map
// are displayed read-only and may not be edited through the form interface,
// because yaml.Marshal would silently discard anchor/alias topology on save.
func buildReadOnlySteps(origBlock string, steps []config.StepConfig) map[int]bool {
	result := make(map[int]bool)
	if len(steps) == 0 || origBlock == "" {
		return result
	}

	lines := strings.Split(origBlock, "\n")
	currentIdx := -1 // index into steps; -1 = not inside a step yet

	// stepIndent is the leading-space count for "- id: <step>" lines.
	// We discover it lazily from the first matching step line.
	stepIndent := -1

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		indent := leadingSpaces(line)

		// Check if this line starts a new step block.
		for i, step := range steps {
			if matchesStepID(trimmed, step.ID) {
				if stepIndent < 0 {
					stepIndent = indent
				}
				currentIdx = i
				break
			}
		}

		if currentIdx < 0 {
			continue
		}

		// A non-blank line shallower than step-list indent means we left the
		// steps section; stop tracking.
		if stepIndent >= 0 && trimmed != "" && indent < stepIndent {
			currentIdx = -1
			continue
		}

		if reAnchorInline.MatchString(line) || reAliasInline.MatchString(line) {
			result[currentIdx] = true
		}
	}

	return result
}

// reAnchorInline / reAliasInline match YAML anchors and aliases inside a line.
var reAnchorInline = regexp.MustCompile(`&\S+`)
var reAliasInline = regexp.MustCompile(`\*\S+`)

// matchesStepID reports whether trimmed is a YAML list-item line that declares
// the given step id, accepting unquoted, double-quoted, and single-quoted forms.
func matchesStepID(trimmed, id string) bool {
	return trimmed == "- id: "+id ||
		trimmed == `- id: "`+id+`"` ||
		trimmed == "- id: '"+id+"'"
}

// workflow returns the workflow being edited (value copy).
func (m *Model) workflow() config.WorkflowConfig {
	return m.cfg.Workflows[m.workflowIdx]
}

// ─── Init ────────────────────────────────────────────────────────────────────

func (m *Model) Init() tea.Cmd {
	return nil
}

// ─── Update ──────────────────────────────────────────────────────────────────

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case savedMsg, errMsg:
		return m.handleSaveResult(msg)
	}
	return m, nil
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// ── form mode ──────────────────────────────────────────────────────────
	if m.form != nil {
		switch key {
		case "esc":
			m.form = nil
			m.setStatus("Edit cancelled.", false)
		case "enter":
			m.applyForm()
		case "tab":
			m.form.NextField()
		case "shift+tab":
			m.form.PrevField()
		case "ctrl+c", "q":
			// pass through to main quit handler
			m.form = nil
		case "backspace", "ctrl+h":
			m.form.HandleBackspace()
		default:
			if len(msg.Runes) == 1 {
				m.form.HandleRune(msg.Runes[0])
			}
		}
		return m, nil
	}

	// ── diff confirm mode ──────────────────────────────────────────────────
	if m.view == viewDiffConfirm {
		switch key {
		case "y", "enter":
			return m, m.saveCmd()
		case "n", "esc":
			m.view = viewGraph
			m.pendingDiff = nil
			m.setStatus("Save cancelled.", false)
		case "j", "down":
			m.yamlScroll++
		case "k", "up":
			if m.yamlScroll > 0 {
				m.yamlScroll--
			}
		}
		return m, nil
	}

	// ── YAML / diff scroll ─────────────────────────────────────────────────
	if m.view == viewYAML || m.view == viewDiff {
		switch key {
		case "j", "down":
			m.yamlScroll++
		case "k", "up":
			if m.yamlScroll > 0 {
				m.yamlScroll--
			}
		case "g":
			m.yamlScroll = 0
		case "G":
			m.yamlScroll = 9999
		case "q", "esc", "tab":
			m.view = viewGraph
			m.yamlScroll = 0
		}
		return m, nil
	}

	// ── graph view ─────────────────────────────────────────────────────────
	switch key {
	case "ctrl+c", "q":
		if m.dirty {
			m.setStatus("Unsaved changes. Press Q again or S to save.", false)
			m.dirty = false // second Q → allow quit
			return m, nil
		}
		return m, tea.Quit

	case "tab", "1":
		m.view = viewGraph
	case "2":
		m.view = viewYAML
		m.yamlScroll = 0
	case "3":
		m.view = viewDiff
		m.yamlScroll = 0

	// Navigation
	case "k", "up":
		m.moveUp()
	case "j", "down":
		m.moveDown()

	// Edit
	case "e", "enter":
		m.openForm()

	// Add step
	case "a":
		m.addStep()

	// Delete step
	case "D":
		m.deleteStep()

	// Save
	case "s":
		m.view = viewDiffConfirm
		m.pendingDiff = m.computeDiff()
		m.yamlScroll = 0

	// Validate and show errors
	case "v":
		m.validate()
	}

	return m, nil
}

func (m *Model) moveUp() {
	if m.target == targetStep {
		if m.stepIdx == 0 {
			m.target = targetTrigger
		} else {
			m.stepIdx--
		}
	}
}

func (m *Model) moveDown() {
	wf := m.workflow()
	if m.target == targetTrigger {
		if len(wf.Steps) > 0 {
			m.target = targetStep
			m.stepIdx = 0
		}
	} else {
		if m.stepIdx < len(wf.Steps)-1 {
			m.stepIdx++
		}
	}
}

func (m *Model) openForm() {
	if m.target == targetTrigger {
		m.form = NewTriggerForm(m.workflow().Trigger)
		return
	}
	wf := m.workflow()
	if m.stepIdx >= len(wf.Steps) {
		return
	}
	if m.readOnlySteps[m.stepIdx] {
		m.setStatus("This step contains unsupported YAML (anchors/aliases) — read-only.", true)
		return
	}
	m.form = NewStepForm(wf.Steps[m.stepIdx])
}

func (m *Model) applyForm() {
	if m.form == nil {
		return
	}
	if m.target == targetTrigger {
		updated := m.form.ApplyTrigger(m.cfg.Workflows[m.workflowIdx].Trigger)
		wf := m.cfg.Workflows[m.workflowIdx]
		wf.Trigger = updated
		m.cfg.Workflows[m.workflowIdx] = wf
	} else {
		updated := m.form.Apply()
		wf := m.cfg.Workflows[m.workflowIdx]
		if m.stepIdx < len(wf.Steps) {
			wf.Steps[m.stepIdx] = updated
			m.cfg.Workflows[m.workflowIdx] = wf
		}
	}
	m.form = nil
	m.dirty = true
	m.setStatus("Changes applied (not yet saved — press S to save).", false)
}

func (m *Model) addStep() {
	newStep := config.StepConfig{
		ID:    fmt.Sprintf("step-%d", len(m.workflow().Steps)+1),
		Agent: "",
	}
	wf := m.cfg.Workflows[m.workflowIdx]
	insertAt := len(wf.Steps)
	if m.target == targetStep {
		insertAt = m.stepIdx + 1
	}
	steps := make([]config.StepConfig, 0, len(wf.Steps)+1)
	steps = append(steps, wf.Steps[:insertAt]...)
	steps = append(steps, newStep)
	steps = append(steps, wf.Steps[insertAt:]...)
	wf.Steps = steps
	m.cfg.Workflows[m.workflowIdx] = wf

	m.target = targetStep
	m.stepIdx = insertAt
	m.dirty = true
	m.openForm()
	m.setStatus("Step added. Fill in the fields and press Enter.", false)
}

func (m *Model) deleteStep() {
	if m.target != targetStep {
		return
	}
	wf := m.cfg.Workflows[m.workflowIdx]
	if m.stepIdx >= len(wf.Steps) {
		return
	}
	steps := make([]config.StepConfig, 0, len(wf.Steps)-1)
	steps = append(steps, wf.Steps[:m.stepIdx]...)
	steps = append(steps, wf.Steps[m.stepIdx+1:]...)
	wf.Steps = steps
	m.cfg.Workflows[m.workflowIdx] = wf

	if m.stepIdx >= len(wf.Steps) && m.stepIdx > 0 {
		m.stepIdx--
	}
	if len(wf.Steps) == 0 {
		m.target = targetTrigger
	}
	m.dirty = true
	m.setStatus("Step deleted.", false)
}

func (m *Model) validate() {
	errs := m.cfg.Validate()
	if len(errs) == 0 {
		m.setStatus("✓ Config is valid.", false)
		return
	}
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		msgs = append(msgs, "✗ "+e.Error())
	}
	m.setStatus(strings.Join(msgs, " | "), true)
}

func (m *Model) computeDiff() []DiffLine {
	newBlock, err := WorkflowToYAML(m.workflow())
	if err != nil {
		return nil
	}
	return ComputeDiff(m.origBlock, newBlock)
}

// saveCmd is a tea.Cmd that writes the modified workflow back to the file.
func (m *Model) saveCmd() tea.Cmd {
	return func() tea.Msg {
		if err := m.save(); err != nil {
			return errMsg{err}
		}
		return savedMsg{}
	}
}

type savedMsg struct{}
type errMsg struct{ err error }

func (m *Model) save() error {
	// Read the current raw file content (re-read to pick up any external edits).
	raw, err := os.ReadFile(m.cfgPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", m.cfgPath, err)
	}
	wf := m.workflow()
	updated, err := ReplaceWorkflowInRaw(string(raw), wf.ID, wf)
	if err != nil {
		// Do NOT fall back to yaml.Marshal(m.cfg): the in-memory Config was
		// populated by config.Load which expands ${VAR} references, so a full
		// re-marshal would leak plaintext secrets and destroy comments.
		return fmt.Errorf("replacing workflow block: %w", err)
	}
	if werr := os.WriteFile(m.cfgPath, []byte(updated), 0o600); werr != nil {
		return fmt.Errorf("writing %s: %w", m.cfgPath, werr)
	}
	// Update the origBlock to the just-saved content.
	m.origBlock, _ = WorkflowToYAML(wf)
	return nil
}

// Update to handle async save result
func (m *Model) handleSaveResult(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case savedMsg:
		m.dirty = false
		m.view = viewGraph
		m.pendingDiff = nil
		m.setStatus("✓ Saved.", false)
	case errMsg:
		m.view = viewGraph
		m.setStatus("✗ Save failed: "+msg.err.Error(), true)
	}
	return m, nil
}

func (m *Model) setStatus(s string, isErr bool) {
	m.statusMsg = s
	m.statusIsErr = isErr
}

// ─── View ────────────────────────────────────────────────────────────────────

func (m *Model) View() string {
	if m.width == 0 {
		return "Loading editor…"
	}

	var sections []string
	sections = append(sections, m.renderHeader())

	switch m.view {
	case viewYAML:
		sections = append(sections, m.renderYAMLPane())
	case viewDiff, viewDiffConfirm:
		sections = append(sections, m.renderDiffPane())
	default:
		sections = append(sections, m.renderGraphView())
	}

	sections = append(sections, m.renderFooter())

	return strings.Join(sections, "\n")
}

func (m *Model) renderHeader() string {
	wf := m.workflow()
	title := styleTitle.Render("apiary edit") + "  " +
		styleBold.Render(wf.ID)
	if wf.Description != "" {
		title += "  " + styleMuted.Render(wf.Description)
	}
	if m.dirty {
		title += "  " + styleWarning.Render("●modified")
	}

	tabBar := ""
	tabs := []struct{ key, label string }{
		{"1", "Graph"},
		{"2", "YAML"},
		{"3", "Diff"},
	}
	for _, t := range tabs {
		active := (t.label == "Graph" && m.view == viewGraph) ||
			(t.label == "YAML" && m.view == viewYAML) ||
			(t.label == "Diff" && (m.view == viewDiff || m.view == viewDiffConfirm))
		if active {
			tabBar += styleFooterKey.Render(" "+t.key+":"+t.label+" ")
		} else {
			tabBar += styleFooterDim.Render(" "+t.key+":"+t.label+" ")
		}
	}

	return title + "  " + tabBar
}

func (m *Model) renderGraphView() string {
	contentH := m.height - 4 // header + footer + status + 1 margin
	if contentH < 4 {
		contentH = 4
	}

	leftW := 36
	if m.width < 80 {
		leftW = m.width / 2
	}
	rightW := m.width - leftW - 1

	wf := m.workflow()

	// Selected step index for graph (-1 = trigger selected)
	graphSelectedIdx := -1
	if m.target == targetStep {
		graphSelectedIdx = m.stepIdx
	}

	// Left panel: graph
	graphStr := RenderGraph(wf, graphSelectedIdx, m.readOnlySteps, leftW)
	leftLines := strings.Split(strings.TrimRight(graphStr, "\n"), "\n")

	// Right panel: form or detail
	var rightContent string
	if m.form != nil {
		rightContent = m.form.Render(rightW)
	} else if m.target == targetTrigger {
		rightContent = RenderTriggerDetail(wf.Trigger, rightW)
	} else if m.target == targetStep && m.stepIdx < len(wf.Steps) {
		rightContent = RenderStepDetail(wf.Steps[m.stepIdx], rightW)

		// Append validation info if any
		errs := m.cfg.Validate()
		stepErrs := filterStepErrors(errs, wf.Steps[m.stepIdx].ID)
		if len(stepErrs) > 0 {
			rightContent += "\n" + styleError.Render("Validation errors:") + "\n"
			for _, e := range stepErrs {
				rightContent += styleError.Render("  ✗ "+e.Error()) + "\n"
			}
		}
	}
	rightLines := strings.Split(strings.TrimRight(rightContent, "\n"), "\n")

	// Unsupported warning strip
	if len(m.unsupported) > 0 {
		warn := styleWarning.Render("⚠ Unsupported YAML constructs detected — affected sections are read-only. ")
		warn += styleMuted.Render(fmt.Sprintf("(%d warning(s))", len(m.unsupported)))
		rightLines = append([]string{warn, ""}, rightLines...)
	}

	// Status line
	statusLine := ""
	if m.statusMsg != "" {
		if m.statusIsErr {
			statusLine = styleError.Render(truncStr(m.statusMsg, m.width-2))
		} else {
			statusLine = styleMuted.Render(truncStr(m.statusMsg, m.width-2))
		}
	}

	// Stitch panels
	sep := styleBorder.Render("│")
	maxLines := len(leftLines)
	if len(rightLines) > maxLines {
		maxLines = len(rightLines)
	}
	// Cap to content height
	visLines := contentH - 1 // -1 for status
	if maxLines > visLines {
		maxLines = visLines
	}

	var body strings.Builder
	for i := 0; i < maxLines; i++ {
		l, r := "", ""
		if i < len(leftLines) {
			l = leftLines[i]
		}
		if i < len(rightLines) {
			r = rightLines[i]
		}
		body.WriteString(fitStr(l, leftW) + sep + fitStr(r, rightW) + "\n")
	}

	body.WriteString(strings.Repeat("─", m.width) + "\n")
	body.WriteString(statusLine)

	return body.String()
}

func (m *Model) renderYAMLPane() string {
	wf := m.workflow()
	yamlStr, err := WorkflowToYAML(wf)
	if err != nil {
		return styleError.Render("YAML serialization error: "+err.Error()) + "\n"
	}

	title := styleTitle.Render("YAML Preview") + styleMuted.Render("  (q/esc to return, j/k to scroll)")
	lines := strings.Split(yamlStr, "\n")
	return m.scrolledContent(title, lines, m.height-4)
}

func (m *Model) renderDiffPane() string {
	var diff []DiffLine
	if m.view == viewDiffConfirm {
		diff = m.pendingDiff
	} else {
		diff = m.computeDiff()
	}

	var title string
	if m.view == viewDiffConfirm {
		title = styleWarning.Render("Review changes — Save?") +
			"  " + styleFooterKey.Render(" Y ") + styleFooterLbl.Render(" yes  ") +
			styleFooterKey.Render(" N ") + styleFooterLbl.Render(" no")
	} else {
		title = styleTitle.Render("Semantic Diff") + styleMuted.Render("  (q/esc to return, j/k to scroll)")
	}

	if !DiffHasChanges(diff) {
		return title + "\n\n" + styleMuted.Render("  No changes.") + "\n"
	}

	rawLines := strings.Split(RenderDiff(diff, m.width-4), "\n")
	return m.scrolledContent(title, rawLines, m.height-4)
}

func (m *Model) scrolledContent(title string, lines []string, height int) string {
	var sb strings.Builder
	sb.WriteString(title + "\n")
	sb.WriteString(styleBorder.Render(strings.Repeat("─", m.width)) + "\n")

	// Guard against very small terminals.
	if height < 2 {
		height = 2
	}
	lo := m.yamlScroll
	hi := lo + height - 2
	if lo > len(lines) {
		lo = len(lines)
	}
	if hi > len(lines) {
		hi = len(lines)
	}
	// Ensure hi >= lo after independent clamping (both can land at len(lines)).
	if hi < lo {
		hi = lo
	}
	for _, l := range lines[lo:hi] {
		sb.WriteString(l + "\n")
	}
	if len(lines) > height-2 {
		hint := fmt.Sprintf("  (%d–%d / %d lines)", lo+1, hi, len(lines))
		sb.WriteString(styleMuted.Render(hint) + "\n")
	}
	return sb.String()
}

func (m *Model) renderFooter() string {
	if m.form != nil {
		return ""
	}
	switch m.view {
	case viewDiffConfirm:
		return styleFooterDim.Render("Y confirm  N cancel  j/k scroll")
	case viewYAML, viewDiff:
		return styleFooterDim.Render("q/esc back  j/k scroll  g top  G bottom")
	default:
		return styleFooterKey.Render(" j/k ") + styleFooterLbl.Render(" navigate  ") +
			styleFooterKey.Render(" e ") + styleFooterLbl.Render(" edit  ") +
			styleFooterKey.Render(" a ") + styleFooterLbl.Render(" add step  ") +
			styleFooterKey.Render(" D ") + styleFooterLbl.Render(" delete  ") +
			styleFooterKey.Render(" v ") + styleFooterLbl.Render(" validate  ") +
			styleFooterKey.Render(" s ") + styleFooterLbl.Render(" save  ") +
			styleFooterKey.Render(" q ") + styleFooterLbl.Render(" quit")
	}
}

// filterStepErrors returns validation errors that mention the given step ID.
func filterStepErrors(errs []error, stepID string) []error {
	var out []error
	for _, e := range errs {
		if strings.Contains(e.Error(), stepID) {
			out = append(out, e)
		}
	}
	return out
}

// Run starts the editor TUI.
func Run(cfg *config.Config, cfgPath string, workflowIdx int, rawYAML string) error {
	m := New(cfg, cfgPath, workflowIdx, rawYAML)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// ReadOnlyStep reports whether the step at the given index is marked read-only
// because its raw YAML contains anchors or aliases. Exposed for testing.
func (m *Model) ReadOnlyStep(idx int) bool {
	return m.readOnlySteps[idx]
}
