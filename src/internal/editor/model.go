package editor

import (
	"github.com/orlandoburli/apiary/internal/config"
)

// EditorConfig is the JSON-serialisable view of an apiary.yaml, shared between
// the HTTP API and the browser. It mirrors the Go config types but uses JSON
// tags and omits runtime-only fields.
type EditorConfig struct {
	Agents    []EditorAgent    `json:"agents"`
	Sources   []EditorSource   `json:"sources"`
	Runners   []EditorRunner   `json:"runners"`
	Workflows []EditorWorkflow `json:"workflows"`
	FilePath  string           `json:"file_path"`
}

type EditorAgent struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Model       string `json:"model"`
	SoulFile    string `json:"soul_file,omitempty"`
	Runner      string `json:"runner,omitempty"`
	MaxWorkers  int    `json:"max_workers,omitempty"`
}

type EditorSource struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type EditorRunner struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Provider string `json:"provider,omitempty"`
}

type EditorWorkflow struct {
	ID             string        `json:"id"`
	Description    string        `json:"description,omitempty"`
	Resume         string        `json:"resume,omitempty"`
	Trigger        *EditorTrigger `json:"trigger,omitempty"`
	Steps          []EditorStep   `json:"steps"`
	OnComplete     *EditorHook   `json:"on_complete,omitempty"`
	OnFail         *EditorHook   `json:"on_fail,omitempty"`
	HasUnsupported bool          `json:"has_unsupported"`
	Env            map[string]string `json:"env,omitempty"`
}

type EditorTrigger struct {
	Priority  int             `json:"priority"`
	Exclusive bool            `json:"exclusive,omitempty"`
	Once      bool            `json:"once,omitempty"`
	Match     EditorTrigMatch `json:"match"`
}

type EditorTrigMatch struct {
	Source             string   `json:"source,omitempty"`
	Labels             []string `json:"labels,omitempty"`
	ExcludeLabels      []string `json:"exclude_labels,omitempty"`
	ExcludeLabelPrefix string   `json:"exclude_label_prefix,omitempty"`
	Types              []string `json:"types,omitempty"`
	States             []string `json:"states,omitempty"`
	TitleRegex         string   `json:"title_regex,omitempty"`
}

type EditorHook struct {
	SetState     string   `json:"set_state,omitempty"`
	AddLabels    []string `json:"add_labels,omitempty"`
	RemoveLabels []string `json:"remove_labels,omitempty"`
}

// EditorStep is one node in the visual DAG.
type EditorStep struct {
	ID        string   `json:"id"`
	Type      string   `json:"type"`
	Name      string   `json:"name,omitempty"`
	Condition string   `json:"condition,omitempty"`
	FailWhen  string   `json:"fail_when,omitempty"`
	DependsOn []string `json:"depends_on,omitempty"`
	SeqDepsOn []string `json:"seq_depends_on,omitempty"`
	// Supported is false for step types or field combinations the visual editor
	// cannot yet represent. The node is shown in read-only mode.
	Supported bool `json:"supported"`

	// agent step
	Agent           string         `json:"agent,omitempty"`
	Model           string         `json:"model,omitempty"`
	Prompt          string         `json:"prompt,omitempty"`
	SummaryPrompt   string         `json:"summary_prompt,omitempty"`
	Idempotent      bool           `json:"idempotent,omitempty"`
	OnPass          *EditorNext    `json:"on_pass,omitempty"`
	OnFail          *EditorOutcome `json:"on_fail,omitempty"`
	OnConflict      *EditorOutcome `json:"on_conflict,omitempty"`
	Publish         string         `json:"publish,omitempty"`
	Spawn           string         `json:"spawn,omitempty"`
	Materialize     string         `json:"materialize,omitempty"`
	ActionClass     string         `json:"action_class,omitempty"`
	OnMissingOutput string         `json:"on_missing_output,omitempty"`

	// split step
	Multi    bool          `json:"multi,omitempty"`
	Branches []EditorBranch `json:"branches,omitempty"`

	// approval step
	Message           string          `json:"message,omitempty"`
	Timeout           string          `json:"timeout,omitempty"`
	ResumeOn          *EditorApprTrig `json:"resume_on,omitempty"`
	AbortOn           *EditorApprTrig `json:"abort_on,omitempty"`
	Approvers         []string        `json:"approvers,omitempty"`
	RequiredApprovals int             `json:"required_approvals,omitempty"`

	// wait_for step
	WaitFor *EditorWaitFor `json:"wait_for,omitempty"`

	// foreach step
	Items       string `json:"items,omitempty"`
	As          string `json:"as,omitempty"`
	Concurrency int    `json:"concurrency,omitempty"`
	MaxItems    int    `json:"max_items,omitempty"`
	FailFast    bool   `json:"fail_fast,omitempty"`

	// workflow (sub-workflow) step
	Workflow string         `json:"workflow,omitempty"`
	Uses     string         `json:"uses,omitempty"`
	With     map[string]any `json:"with,omitempty"`
}

type EditorNext struct {
	Next string `json:"next,omitempty"`
}

type EditorOutcome struct {
	Goto       string `json:"goto,omitempty"`
	MaxRetries int    `json:"max_retries,omitempty"`
}

type EditorBranch struct {
	If   string `json:"if,omitempty"`
	Else bool   `json:"else,omitempty"`
	Goto string `json:"goto"`
}

type EditorApprTrig struct {
	CommentContains string `json:"comment_contains,omitempty"`
	LabelAdded      string `json:"label_added,omitempty"`
	StateChanged    string `json:"state_changed,omitempty"`
}

type EditorWaitFor struct {
	Kind            string   `json:"kind,omitempty"`
	CheckInterval   string   `json:"check_interval,omitempty"`
	MaxDuration     string   `json:"max_duration,omitempty"`
	FailIfNotPassed *bool    `json:"fail_if_not_passed,omitempty"`
	RemoveLabel     string   `json:"remove_label,omitempty"`
	SatisfiedWhen   []string `json:"satisfied_when,omitempty"`
	BlockerLinkType string   `json:"blocker_link_type,omitempty"`
	OnTimeout       string   `json:"on_timeout,omitempty"`
}

// ValidationError is one error returned by the validate endpoint, optionally
// annotated with the workflow and step IDs the error refers to.
type ValidationError struct {
	Message    string `json:"message"`
	WorkflowID string `json:"workflow_id,omitempty"`
	StepID     string `json:"step_id,omitempty"`
}

// configToEditor converts a loaded *config.Config to the editor JSON model.
// rawYAML is the unexpanded file content; it is parsed to detect YAML anchors
// and aliases so that affected workflows can be presented as read-only.
func configToEditor(cfg *config.Config, rawYAML []byte, filePath string) EditorConfig {
	anchored := scanAnchoredWorkflows(rawYAML)
	ec := EditorConfig{FilePath: filePath}
	for _, a := range cfg.Agents {
		ec.Agents = append(ec.Agents, EditorAgent{
			ID: a.ID, Description: a.Description, Model: a.Model,
			SoulFile: a.SoulFile, Runner: a.Runner, MaxWorkers: a.MaxWorkers,
		})
	}
	for _, s := range cfg.Sources {
		ec.Sources = append(ec.Sources, EditorSource{ID: s.ID, Type: s.Type})
	}
	for _, r := range cfg.Runners {
		ec.Runners = append(ec.Runners, EditorRunner{ID: r.ID, Type: r.Type, Provider: r.Provider})
	}
	for _, wf := range cfg.Workflows {
		ec.Workflows = append(ec.Workflows, workflowToEditor(wf, anchored[wf.ID]))
	}
	return ec
}

func workflowToEditor(wf config.WorkflowConfig, hasAnchor bool) EditorWorkflow {
	ew := EditorWorkflow{
		ID:          wf.ID,
		Description: wf.Description,
		Resume:      wf.Resume,
		Env:         wf.Env,
	}
	if wf.Trigger != nil {
		t := wf.Trigger
		ew.Trigger = &EditorTrigger{
			Priority:  t.Priority,
			Exclusive: t.Exclusive,
			Once:      t.Once,
			Match: EditorTrigMatch{
				Source:             t.Match.Source,
				Labels:             t.Match.Labels,
				ExcludeLabels:      t.Match.ExcludeLabels,
				ExcludeLabelPrefix: t.Match.ExcludeLabelPrefix,
				Types:              t.Match.Types,
				States:             t.Match.States,
				TitleRegex:         t.Match.TitleRegex,
			},
		}
	}
	if wf.OnComplete != nil {
		ew.OnComplete = hookToEditor(wf.OnComplete)
	}
	if wf.OnFail != nil {
		ew.OnFail = hookToEditor(wf.OnFail)
	}
	hasUnsupported := hasAnchor
	for _, s := range wf.Steps {
		es := stepToEditor(s)
		if !es.Supported {
			hasUnsupported = true
		}
		ew.Steps = append(ew.Steps, es)
	}
	ew.HasUnsupported = hasUnsupported
	return ew
}

func hookToEditor(h *config.OnComplete) *EditorHook {
	if h == nil {
		return nil
	}
	return &EditorHook{
		SetState:     h.SetState,
		AddLabels:    h.AddLabels,
		RemoveLabels: h.RemoveLabels,
	}
}

// supportedTypes lists the step types the visual editor can fully represent.
var supportedTypes = map[string]bool{
	config.StepTypeAgent:    true,
	config.StepTypeSplit:    true,
	config.StepTypeApproval: true,
	config.StepTypeWaitFor:  true,
	config.StepTypeForeach:  true,
	config.StepTypeWorkflow: true,
	config.StepTypeParallel: true,
}

func stepToEditor(s config.StepConfig) EditorStep {
	t := s.StepType()
	es := EditorStep{
		ID:        s.ID,
		Type:      t,
		Name:      s.Name,
		Condition: s.Condition,
		FailWhen:  s.FailWhen,
		DependsOn: s.DependsOn,
		SeqDepsOn: s.SeqDependsOn,
		Supported: supportedTypes[t] && !s.IsV2Step(),
	}

	switch t {
	case config.StepTypeAgent:
		es.Agent = s.Agent
		es.Model = s.Model
		es.Prompt = s.Prompt
		es.SummaryPrompt = s.SummaryPrompt
		es.Idempotent = s.Idempotent
		es.OnMissingOutput = s.OnMissingOutput
		es.Publish = s.Publish
		es.Spawn = s.Spawn
		es.Materialize = s.Materialize
		es.ActionClass = s.ActionClass
		if s.OnPass != nil {
			es.OnPass = &EditorNext{Next: s.OnPass.Next}
		}
		if s.OnFail != nil {
			es.OnFail = &EditorOutcome{Goto: s.OnFail.Goto, MaxRetries: s.OnFail.MaxRetries}
		}
		if s.OnConflict != nil {
			es.OnConflict = &EditorOutcome{Goto: s.OnConflict.Goto, MaxRetries: s.OnConflict.MaxRetries}
		}

	case config.StepTypeSplit:
		es.Multi = s.Multi
		for _, b := range s.Branches {
			es.Branches = append(es.Branches, EditorBranch{If: b.If, Else: b.Else, Goto: b.Goto})
		}

	case config.StepTypeApproval:
		es.Message = s.Message
		es.Timeout = s.Timeout
		es.Approvers = s.Approvers
		es.RequiredApprovals = s.RequiredApprovals
		if s.ResumeOn != nil {
			es.ResumeOn = &EditorApprTrig{
				CommentContains: s.ResumeOn.CommentContains,
				LabelAdded:      s.ResumeOn.LabelAdded,
				StateChanged:    s.ResumeOn.StateChanged,
			}
		}
		if s.AbortOn != nil {
			es.AbortOn = &EditorApprTrig{
				CommentContains: s.AbortOn.CommentContains,
				LabelAdded:      s.AbortOn.LabelAdded,
				StateChanged:    s.AbortOn.StateChanged,
			}
		}

	case config.StepTypeWaitFor:
		if s.WaitFor != nil {
			w := s.WaitFor
			es.WaitFor = &EditorWaitFor{
				Kind:            w.Kind,
				CheckInterval:   w.CheckInterval,
				MaxDuration:     w.MaxDuration,
				FailIfNotPassed: w.FailIfNotPassed,
				RemoveLabel:     w.RemoveLabel,
				SatisfiedWhen:   w.SatisfiedWhen,
				BlockerLinkType: w.BlockerLinkType,
				OnTimeout:       w.OnTimeout,
			}
		}

	case config.StepTypeForeach:
		es.Items = s.Items
		es.As = s.As
		es.Concurrency = s.Concurrency
		es.MaxItems = s.MaxItems
		es.FailFast = s.FailFast
		// Nested step shown as read-only unsupported.

	case config.StepTypeWorkflow:
		es.Workflow = s.Workflow
		es.Uses = s.Uses
		es.With = s.With
		if s.OnPass != nil {
			es.OnPass = &EditorNext{Next: s.OnPass.Next}
		}
		if s.OnFail != nil {
			es.OnFail = &EditorOutcome{Goto: s.OnFail.Goto, MaxRetries: s.OnFail.MaxRetries}
		}

	case config.StepTypeParallel:
		// Children (SubSteps) are shown but not individually editable.
		es.Supported = false
	}
	return es
}

// editorToWorkflow converts an EditorWorkflow back to a config.WorkflowConfig
// for YAML serialisation.
func editorToWorkflow(ew EditorWorkflow) config.WorkflowConfig {
	wf := config.WorkflowConfig{
		ID:          ew.ID,
		Description: ew.Description,
		Resume:      ew.Resume,
		Env:         ew.Env,
	}
	if ew.Trigger != nil {
		t := ew.Trigger
		wf.Trigger = &config.TriggerConfig{
			Priority:  t.Priority,
			Exclusive: t.Exclusive,
			Once:      t.Once,
			Match: config.RouteMatch{
				Source:             t.Match.Source,
				Labels:             t.Match.Labels,
				ExcludeLabels:      t.Match.ExcludeLabels,
				ExcludeLabelPrefix: t.Match.ExcludeLabelPrefix,
				Types:              t.Match.Types,
				States:             t.Match.States,
				TitleRegex:         t.Match.TitleRegex,
			},
		}
	}
	if ew.OnComplete != nil {
		wf.OnComplete = editorHookToConfig(ew.OnComplete)
	}
	if ew.OnFail != nil {
		wf.OnFail = editorHookToConfig(ew.OnFail)
	}
	for _, es := range ew.Steps {
		wf.Steps = append(wf.Steps, editorToStep(es))
	}
	return wf
}

func editorHookToConfig(h *EditorHook) *config.OnComplete {
	if h == nil {
		return nil
	}
	return &config.OnComplete{
		SetState:     h.SetState,
		AddLabels:    h.AddLabels,
		RemoveLabels: h.RemoveLabels,
	}
}

func editorToStep(es EditorStep) config.StepConfig {
	s := config.StepConfig{
		ID:           es.ID,
		Type:         es.Type,
		Name:         es.Name,
		Condition:    es.Condition,
		FailWhen:     es.FailWhen,
		SeqDependsOn: es.SeqDepsOn,
	}
	switch es.Type {
	case config.StepTypeAgent, "":
		s.Agent = es.Agent
		s.Model = es.Model
		s.Prompt = es.Prompt
		s.SummaryPrompt = es.SummaryPrompt
		s.Idempotent = es.Idempotent
		s.OnMissingOutput = es.OnMissingOutput
		s.Publish = es.Publish
		s.Spawn = es.Spawn
		s.Materialize = es.Materialize
		s.ActionClass = es.ActionClass
		if es.OnPass != nil && es.OnPass.Next != "" {
			s.OnPass = &config.StepNext{Next: es.OnPass.Next}
		}
		if es.OnFail != nil && (es.OnFail.Goto != "" || es.OnFail.MaxRetries > 0) {
			s.OnFail = &config.StepOutcome{Goto: es.OnFail.Goto, MaxRetries: es.OnFail.MaxRetries}
		}
		if es.OnConflict != nil && (es.OnConflict.Goto != "" || es.OnConflict.MaxRetries > 0) {
			s.OnConflict = &config.StepOutcome{Goto: es.OnConflict.Goto, MaxRetries: es.OnConflict.MaxRetries}
		}

	case config.StepTypeSplit:
		s.Multi = es.Multi
		for _, b := range es.Branches {
			s.Branches = append(s.Branches, config.SplitBranch{If: b.If, Else: b.Else, Goto: b.Goto})
		}

	case config.StepTypeApproval:
		s.Message = es.Message
		s.Timeout = es.Timeout
		s.Approvers = es.Approvers
		s.RequiredApprovals = es.RequiredApprovals
		if es.ResumeOn != nil {
			s.ResumeOn = &config.ApprovalTrigger{
				CommentContains: es.ResumeOn.CommentContains,
				LabelAdded:      es.ResumeOn.LabelAdded,
				StateChanged:    es.ResumeOn.StateChanged,
			}
		}
		if es.AbortOn != nil {
			s.AbortOn = &config.ApprovalTrigger{
				CommentContains: es.AbortOn.CommentContains,
				LabelAdded:      es.AbortOn.LabelAdded,
				StateChanged:    es.AbortOn.StateChanged,
			}
		}

	case config.StepTypeWaitFor:
		if es.WaitFor != nil {
			s.WaitFor = &config.WaitForConfig{
				Kind:            es.WaitFor.Kind,
				CheckInterval:   es.WaitFor.CheckInterval,
				MaxDuration:     es.WaitFor.MaxDuration,
				FailIfNotPassed: es.WaitFor.FailIfNotPassed,
				RemoveLabel:     es.WaitFor.RemoveLabel,
				SatisfiedWhen:   es.WaitFor.SatisfiedWhen,
				BlockerLinkType: es.WaitFor.BlockerLinkType,
				OnTimeout:       es.WaitFor.OnTimeout,
			}
		}

	case config.StepTypeForeach:
		s.Items = es.Items
		s.As = es.As
		s.Concurrency = es.Concurrency
		s.MaxItems = es.MaxItems
		s.FailFast = es.FailFast

	case config.StepTypeWorkflow:
		s.Workflow = es.Workflow
		s.Uses = es.Uses
		s.With = es.With
		if es.OnPass != nil && es.OnPass.Next != "" {
			s.OnPass = &config.StepNext{Next: es.OnPass.Next}
		}
		if es.OnFail != nil && (es.OnFail.Goto != "" || es.OnFail.MaxRetries > 0) {
			s.OnFail = &config.StepOutcome{Goto: es.OnFail.Goto, MaxRetries: es.OnFail.MaxRetries}
		}
	}
	return s
}
