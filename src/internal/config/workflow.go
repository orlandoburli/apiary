package config

import "time"

// Step type identifiers. A step with an empty Type is treated as StepTypeAgent.
const (
	StepTypeAgent    = "agent"
	StepTypeSplit    = "split"
	StepTypeApproval = "approval"
	StepTypeForeach  = "foreach"
	StepTypeWorkflow = "workflow"
)

// Resume policy values for a workflow.
const (
	ResumeAllowed   = "allowed"
	ResumeForbidden = "forbidden"
	ResumeAuto      = "auto"
)

// Result comment modes for a workflow.
const (
	ResultCommentOnComplete = "on_complete"
	ResultCommentPerStep    = "per_step"
	ResultCommentOff        = "off"
)

// on_missing_output policy values for an agent step that declares output_schema.
const (
	OnMissingOutputWarn   = "warn"
	OnMissingOutputFail   = "fail"
	OnMissingOutputIgnore = "ignore"
)

// WorkflowConfig defines a multi-step pipeline triggered by matching tasks.
// A plain route is equivalent to a single-step workflow; both are executed by
// the workflow engine.
type WorkflowConfig struct {
	ID          string `yaml:"id"`
	Description string `yaml:"description,omitempty"`
	// Resume controls how a failed/interrupted instance may be restarted:
	// allowed (default), forbidden, or auto. Empty means allowed.
	Resume string `yaml:"resume,omitempty"`
	// ResultComment overrides settings.result_comment for this workflow:
	// on_complete (default), per_step, or off. Empty inherits the global default.
	ResultComment string         `yaml:"result_comment,omitempty"`
	Trigger       *TriggerConfig `yaml:"trigger,omitempty"`
	Steps         []StepConfig   `yaml:"steps"`
	OnComplete    *OnComplete    `yaml:"on_complete,omitempty"`
	OnFail        *OnComplete    `yaml:"on_fail,omitempty"`
}

// ResumePolicy returns the effective resume policy, defaulting to ResumeAllowed.
func (w WorkflowConfig) ResumePolicy() string {
	if w.Resume == "" {
		return ResumeAllowed
	}
	return w.Resume
}

// TriggerConfig selects which tasks start a workflow. It mirrors route matching:
// priority ordering plus a RouteMatch condition.
type TriggerConfig struct {
	Priority int        `yaml:"priority"`
	Match    RouteMatch `yaml:"match"`
}

// StepConfig is one node in a workflow graph. The active fields depend on Type.
type StepConfig struct {
	ID        string   `yaml:"id"`
	Type      string   `yaml:"type,omitempty"` // agent(default)|split|approval|foreach|workflow
	DependsOn []string `yaml:"depends_on,omitempty"`

	// ── agent step ────────────────────────────────────────────────
	Agent           string        `yaml:"agent,omitempty"`
	Model           string        `yaml:"model,omitempty"` // overrides agent's model for this step
	Prompt          string        `yaml:"prompt,omitempty"`
	SummaryPrompt   string        `yaml:"summary_prompt,omitempty"`
	Idempotent      bool          `yaml:"idempotent,omitempty"`
	OutputSchema    *OutputSchema `yaml:"output_schema,omitempty"`
	OnMissingOutput string        `yaml:"on_missing_output,omitempty"` // warn(default)|fail|ignore
	Memory          *MemoryConfig `yaml:"memory,omitempty"`
	OnPass          *StepNext     `yaml:"on_pass,omitempty"`
	OnFail          *StepOutcome  `yaml:"on_fail,omitempty"`

	// ── split step ────────────────────────────────────────────────
	Multi    bool          `yaml:"multi,omitempty"`
	Branches []SplitBranch `yaml:"branches,omitempty"`

	// ── approval step ─────────────────────────────────────────────
	Message  string           `yaml:"message,omitempty"`
	ResumeOn *ApprovalTrigger `yaml:"resume_on,omitempty"`
	AbortOn  *ApprovalTrigger `yaml:"abort_on,omitempty"`
	Timeout  string           `yaml:"timeout,omitempty"`

	// ── foreach step ──────────────────────────────────────────────
	Items       string      `yaml:"items,omitempty"`
	As          string      `yaml:"as,omitempty"`
	Concurrency int         `yaml:"concurrency,omitempty"`
	MaxItems    int         `yaml:"max_items,omitempty"`
	FailFast    bool        `yaml:"fail_fast,omitempty"`
	Step        *StepConfig `yaml:"step,omitempty"`

	// ── sub-workflow step ─────────────────────────────────────────
	Workflow string `yaml:"workflow,omitempty"`
}

// StepType returns the step's type, defaulting to StepTypeAgent when unset.
func (s StepConfig) StepType() string {
	if s.Type == "" {
		return StepTypeAgent
	}
	return s.Type
}

// MemoryReadEnabled reports whether the workflow memory document should be
// injected into this step. Defaults to true; only an explicit `memory.read: false`
// disables it.
func (s StepConfig) MemoryReadEnabled() bool {
	if s.Memory == nil || s.Memory.Read == nil {
		return true
	}
	return *s.Memory.Read
}

// MemoryWriteFields returns the output_schema field names this step persists to
// workflow memory, or nil when none are declared.
func (s StepConfig) MemoryWriteFields() []string {
	if s.Memory == nil {
		return nil
	}
	return s.Memory.Write
}

// ParsedTimeout returns the approval-step timeout, or 0 when unset/invalid
// (meaning no timeout).
func (s StepConfig) ParsedTimeout() time.Duration {
	if s.Timeout == "" {
		return 0
	}
	d, err := time.ParseDuration(s.Timeout)
	if err != nil {
		return 0
	}
	return d
}

// MemoryConfig controls how a step interacts with the workflow memory object.
type MemoryConfig struct {
	// Read is a pointer so an explicit `read: false` is distinguishable from an
	// unset value (which defaults to true). Use StepConfig.MemoryReadEnabled().
	Read  *bool    `yaml:"read,omitempty"`
	Write []string `yaml:"write,omitempty"`
}

// StepNext is the explicit success edge of an agent step (on_pass.next).
type StepNext struct {
	Next string `yaml:"next,omitempty"`
}

// StepOutcome is the failure edge of an agent step (on_fail.goto + max_retries).
type StepOutcome struct {
	Goto       string `yaml:"goto,omitempty"`
	MaxRetries int    `yaml:"max_retries,omitempty"`
}

// SplitBranch is one conditional edge of a split step. A branch is the fallback
// ("else") when Else is true or when If is empty.
type SplitBranch struct {
	If   string `yaml:"if,omitempty"`
	Else bool   `yaml:"else,omitempty"`
	Goto string `yaml:"goto"`
}

// IsFallback reports whether this branch is the catch-all/else branch.
func (b SplitBranch) IsFallback() bool {
	return b.Else || b.If == ""
}

// ApprovalTrigger is a resume/abort condition for an approval step. Any single
// populated field that matches the live task is sufficient (OR semantics).
type ApprovalTrigger struct {
	CommentContains string `yaml:"comment_contains,omitempty"`
	LabelAdded      string `yaml:"label_added,omitempty"`
	StateChanged    string `yaml:"state_changed,omitempty"`
}

// IsEmpty reports whether the trigger declares no condition at all.
func (t ApprovalTrigger) IsEmpty() bool {
	return t.CommentContains == "" && t.LabelAdded == "" && t.StateChanged == ""
}

// OutputSchema is the supported JSON Schema subset for structured step output.
type OutputSchema struct {
	Type       string                 `yaml:"type"`
	Properties map[string]SchemaField `yaml:"properties,omitempty"`
	Required   []string               `yaml:"required,omitempty"`
}

// SchemaField describes one property within an OutputSchema. Items is set for
// arrays; Properties/Required are set for nested objects.
type SchemaField struct {
	Type       string                 `yaml:"type"`
	Enum       []string               `yaml:"enum,omitempty"`
	Items      *SchemaField           `yaml:"items,omitempty"`
	Properties map[string]SchemaField `yaml:"properties,omitempty"`
	Required   []string               `yaml:"required,omitempty"`
}
