package editor

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/orlandoburli/apiary/internal/config"
)

// palette — same values as dashboard/styles.go so the editor feels consistent
var (
	colAccent  = lipgloss.Color("117")
	colMuted   = lipgloss.Color("244")
	colInfo    = lipgloss.Color("39")
	colWarning = lipgloss.Color("220")
	colError   = lipgloss.Color("203")
	colSuccess = lipgloss.Color("42")
	colFocused = lipgloss.Color("213")
	colSelBg   = lipgloss.Color("24")
	colText    = lipgloss.Color("252")
	colBorder  = lipgloss.Color("60")
	colTitle   = lipgloss.Color("81")

	styleAccent     = lipgloss.NewStyle().Foreground(colAccent)
	styleMuted      = lipgloss.NewStyle().Foreground(colMuted)
	styleInfo       = lipgloss.NewStyle().Foreground(colInfo)
	styleWarning    = lipgloss.NewStyle().Foreground(colWarning)
	styleError      = lipgloss.NewStyle().Foreground(colError)
	styleSuccess    = lipgloss.NewStyle().Foreground(colSuccess)
	styleFocused    = lipgloss.NewStyle().Foreground(colFocused)
	styleSelected   = lipgloss.NewStyle().Background(colSelBg).Foreground(lipgloss.Color("231"))
	styleText       = lipgloss.NewStyle().Foreground(colText)
	styleBold       = lipgloss.NewStyle().Bold(true).Foreground(colText)
	styleBorder     = lipgloss.NewStyle().Foreground(colBorder)
	styleTitle      = lipgloss.NewStyle().Bold(true).Foreground(colTitle)
	styleFooterKey  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Background(lipgloss.Color("57"))
	styleFooterLbl  = lipgloss.NewStyle().Foreground(colText)
	styleFooterDim  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

// RenderGraph renders the left-panel step list with ASCII graph connectors.
// width is the panel width in terminal columns.
// selectedIdx is the index of the currently selected step (-1 for trigger).
func RenderGraph(wf config.WorkflowConfig, selectedIdx int, readOnlySteps map[int]bool, width int) string {
	var sb strings.Builder

	// Trigger node
	trig := formatTrigger(wf.Trigger, width)
	if selectedIdx == -1 {
		trig = styleSelected.Render(fitStr(trig, width))
	}
	sb.WriteString(trig + "\n")

	if len(wf.Steps) == 0 {
		sb.WriteString(styleMuted.Render("  (no steps)") + "\n")
		return sb.String()
	}

	// Build a step-ID → index map for connection rendering
	idxByID := make(map[string]int, len(wf.Steps))
	for i, s := range wf.Steps {
		idxByID[s.ID] = i
	}

	for i, step := range wf.Steps {
		// Connector from trigger or previous step
		conn := styleBorder.Render("       │")
		sb.WriteString(conn + "\n")

		// Step node
		selected := i == selectedIdx
		ro := readOnlySteps[i]
		line := formatStep(step, selected, ro, width)
		sb.WriteString(line + "\n")

		// Back-edge annotation for on_fail.goto loops
		if step.OnFail != nil && step.OnFail.Goto != "" {
			arrow := fmt.Sprintf("       ↺ on_fail → %s", step.OnFail.Goto)
			if step.OnFail.MaxRetries > 0 {
				arrow += fmt.Sprintf(" (max %d)", step.OnFail.MaxRetries)
			}
			sb.WriteString(styleMuted.Render(fitStr(arrow, width)) + "\n")
		}

		// on_pass.next annotation when it differs from sequential order
		if step.OnPass != nil && step.OnPass.Next != "" {
			next := step.OnPass.Next
			// Only annotate when it's non-sequential
			sequential := ""
			if i+1 < len(wf.Steps) {
				sequential = wf.Steps[i+1].ID
			}
			if next != sequential {
				arrow := fmt.Sprintf("       → on_pass → %s", next)
				sb.WriteString(styleAccent.Render(fitStr(arrow, width)) + "\n")
			}
		}

		// Split branches
		if step.StepType() == config.StepTypeSplit {
			for _, br := range step.Branches {
				label := br.Goto
				cond := br.If
				if br.Else || cond == "" {
					cond = "else"
				}
				brLine := fmt.Sprintf("         ├─ if %s → %s", cond, label)
				sb.WriteString(styleMuted.Render(fitStr(brLine, width)) + "\n")
			}
		}
	}

	// End marker
	sb.WriteString(styleBorder.Render("       │") + "\n")
	sb.WriteString(styleMuted.Render("     [end]") + "\n")

	return sb.String()
}

func formatTrigger(t *config.TriggerConfig, width int) string {
	if t == nil {
		return styleMuted.Render(fitStr("  [no trigger — subworkflow or manual]", width))
	}
	parts := []string{"src:" + t.Match.Source}
	if len(t.Match.Labels) > 0 {
		parts = append(parts, "labels:"+strings.Join(t.Match.Labels, ","))
	}
	if t.Priority > 0 {
		parts = append(parts, fmt.Sprintf("prio:%d", t.Priority))
	}
	inner := strings.Join(parts, " ")
	line := fmt.Sprintf("  [trigger] %s", inner)
	return styleInfo.Render(fitStr(line, width))
}

func formatStep(s config.StepConfig, selected, readOnly bool, width int) string {
	typeBadge := stepTypeBadge(s.StepType())
	roMark := ""
	if readOnly {
		roMark = " " + styleMuted.Render("[ro]")
	}

	agentPart := ""
	switch s.StepType() {
	case config.StepTypeAgent, "":
		if s.Agent != "" {
			agentPart = " → " + s.Agent
		}
	case config.StepTypeWorkflow:
		ref := s.Workflow
		if ref == "" {
			ref = s.Uses
		}
		agentPart = " ⤷ " + ref
	case config.StepTypeForeach:
		agentPart = " ∀ " + s.Items
	}

	condPart := ""
	if s.Condition != "" {
		condPart = " if:(" + truncStr(s.Condition, 20) + ")"
	} else if s.If != "" {
		condPart = " if:(" + truncStr(s.If, 20) + ")"
	}

	line := fmt.Sprintf("  %s %s%s%s%s", typeBadge, s.ID, agentPart, condPart, roMark)

	if selected {
		line = styleFocused.Render("▶") + " " + styleSelected.Render(fitStr(stripAnsi(line[2:]), width-2))
	} else {
		line = fitStr(line, width)
	}
	return line
}

func stepTypeBadge(t string) string {
	switch t {
	case config.StepTypeAgent, "":
		return styleInfo.Render("[agent]   ")
	case config.StepTypeApproval:
		return styleWarning.Render("[approval]")
	case config.StepTypeForeach:
		return styleAccent.Render("[foreach] ")
	case config.StepTypeParallel:
		return styleAccent.Render("[parallel]")
	case config.StepTypeSplit:
		return styleMuted.Render("[split]   ")
	case config.StepTypeWorkflow:
		return styleMuted.Render("[workflow]")
	case config.StepTypeWaitFor:
		return styleMuted.Render("[wait_for]")
	default:
		return styleMuted.Render("[" + padStr(t, 8) + "]")
	}
}

// RenderStepDetail renders the right panel for a selected step (read view).
func RenderStepDetail(step config.StepConfig, width int) string {
	var sb strings.Builder

	row := func(label, value string) {
		lbl := styleAccent.Render(padStr(label+":", 16))
		sb.WriteString("  " + lbl + " " + value + "\n")
	}

	sb.WriteString(styleBold.Render(step.ID) + "  " + styleInfo.Render(step.StepType()) + "\n")
	sb.WriteString(styleMuted.Render(strings.Repeat("─", width-2)) + "\n")

	if step.Name != "" {
		row("name", step.Name)
	}

	switch step.StepType() {
	case config.StepTypeAgent, "":
		if step.Agent != "" {
			row("agent", styleAccent.Render(step.Agent))
		}
		if step.Model != "" {
			row("model", step.Model)
		}
		if step.Condition != "" {
			row("condition", truncStr(step.Condition, width-20))
		}
		if step.If != "" {
			row("if (v2)", truncStr(step.If, width-20))
		}
		if step.Prompt != "" {
			sb.WriteString("  " + styleAccent.Render(padStr("prompt:", 16)) + "\n")
			for _, line := range wrapStr(step.Prompt, width-4) {
				sb.WriteString("    " + line + "\n")
			}
		}
		if step.OnPass != nil && step.OnPass.Next != "" {
			row("on_pass.next", styleSuccess.Render(step.OnPass.Next))
		}
		if step.OnFail != nil {
			val := ""
			if step.OnFail.Goto != "" {
				val = styleError.Render("→ " + step.OnFail.Goto)
			}
			if step.OnFail.MaxRetries > 0 {
				val += fmt.Sprintf("  (max %d)", step.OnFail.MaxRetries)
			}
			if val != "" {
				row("on_fail", val)
			}
		}
		if step.FailWhen != "" {
			row("fail_when", truncStr(step.FailWhen, width-20))
		}
		if step.Idempotent {
			row("idempotent", styleSuccess.Render("true"))
		}

	case config.StepTypeApproval:
		if step.Message != "" {
			row("message", truncStr(step.Message, width-20))
		}
		if step.Timeout != "" {
			row("timeout", step.Timeout)
		}
		if len(step.Approvers) > 0 {
			row("approvers", strings.Join(step.Approvers, ", "))
		}
		if step.ResumeOn != nil {
			if step.ResumeOn.CommentContains != "" {
				row("resume_on.comment", step.ResumeOn.CommentContains)
			}
			if step.ResumeOn.LabelAdded != "" {
				row("resume_on.label", step.ResumeOn.LabelAdded)
			}
		}
		if step.AbortOn != nil {
			if step.AbortOn.CommentContains != "" {
				row("abort_on.comment", step.AbortOn.CommentContains)
			}
		}

	case config.StepTypeSplit:
		sb.WriteString("  " + styleAccent.Render("branches:") + "\n")
		for _, br := range step.Branches {
			cond := br.If
			if br.Else || cond == "" {
				cond = "(else)"
			}
			sb.WriteString(fmt.Sprintf("    %-24s → %s\n", styleMuted.Render(cond), styleAccent.Render(br.Goto)))
		}

	case config.StepTypeForeach:
		row("items", step.Items)
		if step.As != "" {
			row("as", step.As)
		}
		if step.Concurrency > 0 {
			row("concurrency", fmt.Sprintf("%d", step.Concurrency))
		}
		if step.MaxItems > 0 {
			row("max_items", fmt.Sprintf("%d", step.MaxItems))
		}

	case config.StepTypeWorkflow:
		ref := step.Workflow
		if ref == "" {
			ref = step.Uses
		}
		row("workflow/uses", ref)
		if len(step.With) > 0 {
			sb.WriteString("  " + styleAccent.Render(padStr("with:", 16)) + "\n")
			for k, v := range step.With {
				sb.WriteString(fmt.Sprintf("    %s: %v\n", k, v))
			}
		}

	case config.StepTypeWaitFor:
		if step.WaitFor != nil {
			row("kind", step.WaitFor.Kind)
			if step.WaitFor.CheckInterval != "" {
				row("check_interval", step.WaitFor.CheckInterval)
			}
			if step.WaitFor.MaxDuration != "" {
				row("max_duration", step.WaitFor.MaxDuration)
			}
		}

	case config.StepTypeParallel:
		sb.WriteString("  " + styleAccent.Render("parallel steps:") + "\n")
		for _, sub := range step.SubSteps {
			sb.WriteString(fmt.Sprintf("    - %s (%s)\n", sub.ID, sub.StepType()))
		}
	}

	if step.Memory != nil {
		parts := []string{}
		if step.Memory.Read != nil && !*step.Memory.Read {
			parts = append(parts, "read:off")
		}
		if len(step.Memory.Write) > 0 {
			parts = append(parts, "write:"+strings.Join(step.Memory.Write, ","))
		}
		if len(parts) > 0 {
			row("memory", strings.Join(parts, " "))
		}
	}

	if len(step.Env) > 0 {
		envKeys := make([]string, 0, len(step.Env))
		for k := range step.Env {
			envKeys = append(envKeys, k)
		}
		row("env", strings.Join(envKeys, ", "))
	}

	return sb.String()
}

// RenderTriggerDetail renders the right panel for the trigger.
func RenderTriggerDetail(t *config.TriggerConfig, width int) string {
	if t == nil {
		return styleMuted.Render("  No trigger defined (subworkflow or manual dispatch).") + "\n"
	}
	var sb strings.Builder
	row := func(label, value string) {
		lbl := styleAccent.Render(padStr(label+":", 16))
		sb.WriteString("  " + lbl + " " + value + "\n")
	}
	sb.WriteString(styleBold.Render("trigger") + "\n")
	sb.WriteString(styleMuted.Render(strings.Repeat("─", width-2)) + "\n")
	if t.Match.Source != "" {
		row("source", t.Match.Source)
	}
	if len(t.Match.Labels) > 0 {
		row("labels", strings.Join(t.Match.Labels, ", "))
	}
	if t.Priority != 0 {
		row("priority", fmt.Sprintf("%d", t.Priority))
	}
	if t.Exclusive {
		row("exclusive", styleWarning.Render("true"))
	}
	if t.Once {
		row("once", styleSuccess.Render("true"))
	}
	return sb.String()
}

// helper functions ---------------------------------------------------------

func fitStr(s string, width int) string {
	visible := visibleLen(s)
	if width <= 0 {
		return s
	}
	if visible <= width {
		return s + strings.Repeat(" ", width-visible)
	}
	// truncate without stripping ANSI (caller strips when needed)
	runes := []rune(s)
	if len(runes) > width {
		return string(runes[:width-1]) + "…"
	}
	return s
}

func padStr(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

func truncStr(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}

func wrapStr(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	words := strings.Fields(s)
	var lines []string
	var cur strings.Builder
	for _, w := range words {
		if cur.Len() > 0 && cur.Len()+1+len(w) > width {
			lines = append(lines, cur.String())
			cur.Reset()
		}
		if cur.Len() > 0 {
			cur.WriteByte(' ')
		}
		cur.WriteString(w)
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	return lines
}

// visibleLen returns the number of visible (non-ANSI) characters.
func visibleLen(s string) int {
	return len([]rune(stripAnsi(s)))
}

// stripAnsi removes ANSI escape sequences from s.
func stripAnsi(s string) string {
	var b strings.Builder
	inEscape := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inEscape {
			if c == 'm' {
				inEscape = false
			}
			continue
		}
		if c == '\033' && i+1 < len(s) && s[i+1] == '[' {
			inEscape = true
			i++ // skip '['
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}
