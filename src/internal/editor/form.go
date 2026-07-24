package editor

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/orlandoburli/apiary/internal/config"
)

// Field is one editable field in the step form.
type Field struct {
	Label   string
	Key     string // internal key for mapping back to the struct
	Value   string // current string value
	Help    string // shown below the field
	ReadOnly bool
}

// Form holds the state for inline step editing.
type Form struct {
	Step      config.StepConfig
	Fields    []Field
	ActiveIdx int // currently focused field
	Dirty     bool
}

// NewStepForm creates a form pre-populated from the given step.
func NewStepForm(step config.StepConfig) *Form {
	fields := buildFields(step)
	return &Form{
		Step:   step,
		Fields: fields,
	}
}

// NewTriggerForm creates a form for the trigger block.
// It returns nil when the trigger is nil (subworkflow — no trigger to edit).
func NewTriggerForm(t *config.TriggerConfig) *Form {
	if t == nil {
		t = &config.TriggerConfig{}
	}
	fields := []Field{
		{Label: "Source", Key: "source", Value: t.Match.Source, Help: "source ID (e.g. github)"},
		{Label: "Labels (csv)", Key: "labels", Value: strings.Join(t.Match.Labels, ","), Help: "comma-separated trigger labels"},
		{Label: "Priority", Key: "priority", Value: fmt.Sprintf("%d", t.Priority), Help: "numeric priority (higher = preferred)"},
		{Label: "Exclusive", Key: "exclusive", Value: boolStr(t.Exclusive), Help: "true|false — stop after this trigger matches"},
		{Label: "Once", Key: "once", Value: boolStr(t.Once), Help: "true|false — run at most once per task"},
	}
	return &Form{Fields: fields}
}

// Apply copies the edited field values back into the form's Step.
// Returns the updated StepConfig.
func (f *Form) Apply() config.StepConfig {
	s := f.Step
	for _, field := range f.Fields {
		if field.ReadOnly {
			continue
		}
		applyField(&s, field.Key, field.Value)
	}
	return s
}

// ApplyTrigger copies edited field values back into a TriggerConfig.
func (f *Form) ApplyTrigger(t *config.TriggerConfig) *config.TriggerConfig {
	if t == nil {
		t = &config.TriggerConfig{}
	}
	for _, field := range f.Fields {
		switch field.Key {
		case "source":
			t.Match.Source = field.Value
		case "labels":
			if field.Value == "" {
				t.Match.Labels = nil
			} else {
				t.Match.Labels = strings.Split(field.Value, ",")
				for i := range t.Match.Labels {
					t.Match.Labels[i] = strings.TrimSpace(t.Match.Labels[i])
				}
			}
		case "priority":
			t.Priority, _ = strconv.Atoi(field.Value)
		case "exclusive":
			t.Exclusive = parseBool(field.Value)
		case "once":
			t.Once = parseBool(field.Value)
		}
	}
	return t
}

// HandleRune appends a rune to the active field's value.
func (f *Form) HandleRune(r rune) {
	if f.ActiveIdx >= len(f.Fields) {
		return
	}
	if f.Fields[f.ActiveIdx].ReadOnly {
		return
	}
	f.Fields[f.ActiveIdx].Value += string(r)
	f.Dirty = true
}

// HandleBackspace deletes the last character of the active field.
func (f *Form) HandleBackspace() {
	if f.ActiveIdx >= len(f.Fields) {
		return
	}
	if f.Fields[f.ActiveIdx].ReadOnly {
		return
	}
	v := []rune(f.Fields[f.ActiveIdx].Value)
	if len(v) > 0 {
		f.Fields[f.ActiveIdx].Value = string(v[:len(v)-1])
		f.Dirty = true
	}
}

// NextField moves focus to the next field (wraps).
func (f *Form) NextField() {
	f.ActiveIdx = (f.ActiveIdx + 1) % len(f.Fields)
}

// PrevField moves focus to the previous field (wraps).
func (f *Form) PrevField() {
	f.ActiveIdx = (f.ActiveIdx - 1 + len(f.Fields)) % len(f.Fields)
}

// Render returns the form as a terminal string.
func (f *Form) Render(width int) string {
	var sb strings.Builder
	sb.WriteString(styleTitle.Render("Edit Step") + "\n")
	sb.WriteString(styleMuted.Render(strings.Repeat("─", width-2)) + "\n")

	for i, field := range f.Fields {
		active := i == f.ActiveIdx
		labelStyle := styleMuted
		if active {
			labelStyle = styleFocused
		}

		label := labelStyle.Render(padStr(field.Label+":", 18))
		var valueStr string
		if field.ReadOnly {
			valueStr = styleMuted.Render(field.Value)
		} else if active {
			valueStr = styleSelected.Render(field.Value + "█")
		} else {
			valueStr = styleText.Render(field.Value)
		}

		sb.WriteString("  " + label + " " + valueStr + "\n")
		if active && field.Help != "" {
			sb.WriteString("                    " + styleMuted.Render(field.Help) + "\n")
		}
	}

	sb.WriteString("\n")
	help := styleFooterKey.Render(" Tab ") + styleFooterLbl.Render(" next field  ") +
		styleFooterKey.Render(" Enter ") + styleFooterLbl.Render(" apply  ") +
		styleFooterKey.Render(" Esc ") + styleFooterLbl.Render(" cancel")
	sb.WriteString(help + "\n")

	return sb.String()
}

// buildFields returns the editable fields for a step based on its type.
func buildFields(s config.StepConfig) []Field {
	base := []Field{
		{Label: "ID", Key: "id", Value: s.ID, Help: "unique step identifier"},
		{Label: "Name", Key: "name", Value: s.Name, Help: "human-readable label (optional)"},
		{Label: "Type", Key: "type", Value: s.Type, Help: "agent|split|approval|foreach|workflow|wait_for|parallel"},
	}

	typ := s.StepType()

	switch typ {
	case config.StepTypeAgent, "":
		base = append(base,
			Field{Label: "Agent", Key: "agent", Value: s.Agent, Help: "agent ID from config"},
			Field{Label: "Model", Key: "model", Value: s.Model, Help: "override agent's model (optional)"},
			Field{Label: "Prompt", Key: "prompt", Value: s.Prompt, Help: "instructions for the agent"},
			Field{Label: "If (condition)", Key: "if", Value: cond(s), Help: "skip when false (expression)"},
			Field{Label: "on_pass.next", Key: "on_pass_next", Value: onPassNext(s), Help: "step to run on success (default: next)"},
			Field{Label: "on_fail.goto", Key: "on_fail_goto", Value: onFailGoto(s), Help: "step to retry on failure"},
			Field{Label: "on_fail.retries", Key: "on_fail_retries", Value: onFailRetries(s), Help: "max retry count"},
			Field{Label: "Idempotent", Key: "idempotent", Value: boolStr(s.Idempotent), Help: "true|false"},
			Field{Label: "Action class", Key: "action_class", Value: s.ActionClass, Help: "push|deploy|destructive|publication"},
		)

	case config.StepTypeApproval:
		base = append(base,
			Field{Label: "Message", Key: "message", Value: s.Message, Help: "message shown to approvers"},
			Field{Label: "Timeout", Key: "timeout", Value: s.Timeout, Help: "e.g. 24h"},
			Field{Label: "Approvers (csv)", Key: "approvers", Value: strings.Join(s.Approvers, ","), Help: "github usernames"},
			Field{Label: "resume_on.label", Key: "resume_label", Value: resumeLabel(s), Help: "label that resumes the workflow"},
			Field{Label: "abort_on.label", Key: "abort_label", Value: abortLabel(s), Help: "label that aborts the workflow"},
		)

	case config.StepTypeSplit:
		// Branches are complex nested structures — show read-only with an explanation.
		base = append(base,
			Field{Label: "Multi", Key: "multi", Value: boolStr(s.Multi), Help: "true = all matching branches run"},
			Field{
				Label:    "Branches",
				Key:      "branches",
				Value:    formatBranchesInline(s.Branches),
				Help:     "edit YAML directly to modify branches",
				ReadOnly: true,
			},
		)

	case config.StepTypeForeach:
		base = append(base,
			Field{Label: "Items", Key: "items", Value: s.Items, Help: "expression yielding the list"},
			Field{Label: "As", Key: "as", Value: s.As, Help: "loop variable name"},
			Field{Label: "Concurrency", Key: "concurrency", Value: intStr(s.Concurrency), Help: "max parallel iterations (0 = sequential)"},
			Field{Label: "Max items", Key: "max_items", Value: intStr(s.MaxItems), Help: "0 = unlimited"},
			Field{Label: "Fail fast", Key: "fail_fast", Value: boolStr(s.FailFast), Help: "true|false"},
		)

	case config.StepTypeWorkflow:
		ref := s.Workflow
		if ref == "" {
			ref = s.Uses
		}
		base = append(base,
			Field{Label: "Workflow / Uses", Key: "workflow_ref", Value: ref, Help: "workflow ID or relative file path"},
		)

	case config.StepTypeWaitFor:
		kind := ""
		interval := ""
		maxDur := ""
		if s.WaitFor != nil {
			kind = s.WaitFor.Kind
			interval = s.WaitFor.CheckInterval
			maxDur = s.WaitFor.MaxDuration
		}
		base = append(base,
			Field{Label: "Kind", Key: "wait_kind", Value: kind, Help: "ci | dependency"},
			Field{Label: "Check interval", Key: "wait_interval", Value: interval, Help: "e.g. 1m"},
			Field{Label: "Max duration", Key: "wait_max_dur", Value: maxDur, Help: "e.g. 2h"},
		)
	}

	return base
}

// applyField writes one field value back into the StepConfig.
func applyField(s *config.StepConfig, key, value string) {
	switch key {
	case "id":
		s.ID = value
	case "name":
		s.Name = value
	case "type":
		if value == config.StepTypeAgent {
			s.Type = ""
		} else {
			s.Type = value
		}
	case "agent":
		s.Agent = value
	case "model":
		s.Model = value
	case "prompt":
		s.Prompt = value
	case "if":
		// v2 authored "if:" field
		s.If = value
		s.Condition = "" // clear lowered form
	case "on_pass_next":
		if value == "" {
			s.OnPass = nil
		} else {
			s.OnPass = &config.StepNext{Next: value}
		}
	case "on_fail_goto":
		if s.OnFail == nil {
			s.OnFail = &config.StepOutcome{}
		}
		s.OnFail.Goto = value
		if s.OnFail.Goto == "" && s.OnFail.MaxRetries == 0 {
			s.OnFail = nil
		}
	case "on_fail_retries":
		n, _ := strconv.Atoi(value)
		if s.OnFail == nil {
			s.OnFail = &config.StepOutcome{}
		}
		s.OnFail.MaxRetries = n
		if s.OnFail.Goto == "" && n == 0 {
			s.OnFail = nil
		}
	case "idempotent":
		s.Idempotent = parseBool(value)
	case "action_class":
		s.ActionClass = value
	case "message":
		s.Message = value
	case "timeout":
		s.Timeout = value
	case "approvers":
		if value == "" {
			s.Approvers = nil
		} else {
			parts := strings.Split(value, ",")
			s.Approvers = make([]string, 0, len(parts))
			for _, p := range parts {
				if t := strings.TrimSpace(p); t != "" {
					s.Approvers = append(s.Approvers, t)
				}
			}
		}
	case "resume_label":
		if value == "" {
			if s.ResumeOn != nil && s.ResumeOn.CommentContains == "" && s.ResumeOn.StateChanged == "" {
				s.ResumeOn = nil
			}
		} else {
			if s.ResumeOn == nil {
				s.ResumeOn = &config.ApprovalTrigger{}
			}
			s.ResumeOn.LabelAdded = value
		}
	case "abort_label":
		if value == "" {
			if s.AbortOn != nil && s.AbortOn.CommentContains == "" && s.AbortOn.StateChanged == "" {
				s.AbortOn = nil
			}
		} else {
			if s.AbortOn == nil {
				s.AbortOn = &config.ApprovalTrigger{}
			}
			s.AbortOn.LabelAdded = value
		}
	case "multi":
		s.Multi = parseBool(value)
	case "items":
		s.Items = value
	case "as":
		s.As = value
	case "concurrency":
		s.Concurrency, _ = strconv.Atoi(value)
	case "max_items":
		s.MaxItems, _ = strconv.Atoi(value)
	case "fail_fast":
		s.FailFast = parseBool(value)
	case "workflow_ref":
		// Decide whether this is an inline workflow ID or a file path.
		if strings.Contains(value, "/") || strings.HasSuffix(value, ".yaml") || strings.HasSuffix(value, ".yml") {
			s.Uses = value
			s.Workflow = ""
		} else {
			s.Workflow = value
			s.Uses = ""
		}
	case "wait_kind":
		if s.WaitFor == nil {
			s.WaitFor = &config.WaitForConfig{}
		}
		s.WaitFor.Kind = value
	case "wait_interval":
		if s.WaitFor == nil {
			s.WaitFor = &config.WaitForConfig{}
		}
		s.WaitFor.CheckInterval = value
	case "wait_max_dur":
		if s.WaitFor == nil {
			s.WaitFor = &config.WaitForConfig{}
		}
		s.WaitFor.MaxDuration = value
	}
}

// helpers

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return ""
}

func parseBool(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "true" || s == "yes" || s == "1"
}

func intStr(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func cond(s config.StepConfig) string {
	if s.If != "" {
		return s.If
	}
	return s.Condition
}

func onPassNext(s config.StepConfig) string {
	if s.OnPass != nil {
		return s.OnPass.Next
	}
	return ""
}

func onFailGoto(s config.StepConfig) string {
	if s.OnFail != nil {
		return s.OnFail.Goto
	}
	return ""
}

func onFailRetries(s config.StepConfig) string {
	if s.OnFail != nil && s.OnFail.MaxRetries > 0 {
		return strconv.Itoa(s.OnFail.MaxRetries)
	}
	return ""
}

func resumeLabel(s config.StepConfig) string {
	if s.ResumeOn != nil {
		return s.ResumeOn.LabelAdded
	}
	return ""
}

func abortLabel(s config.StepConfig) string {
	if s.AbortOn != nil {
		return s.AbortOn.LabelAdded
	}
	return ""
}

func formatBranchesInline(branches []config.SplitBranch) string {
	parts := make([]string, 0, len(branches))
	for _, b := range branches {
		cond := b.If
		if b.Else || cond == "" {
			cond = "else"
		}
		parts = append(parts, fmt.Sprintf("%s→%s", cond, b.Goto))
	}
	return strings.Join(parts, "; ")
}
