package config

import "time"

// Step type identifiers. A step with an empty Type is treated as StepTypeAgent.
const (
	StepTypeAgent    = "agent"
	StepTypeSplit    = "split"
	StepTypeApproval = "approval"
	StepTypeForeach  = "foreach"
	StepTypeWorkflow = "workflow"
	// StepTypeParallel is emitted by the v2 lowering pass for `parallel:` steps.
	// Children run concurrently (§8e); the step's outcome = the join policy.
	StepTypeParallel = "parallel"
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

// Publish modes for an agent step: whether the engine writes an APIARY_PUBLISH
// payload emitted by the agent back to the task's source bindings.
const (
	PublishAuto = "auto" // default: write back when the agent emits a payload
	PublishOff  = "off"  // never write back, even if a payload is present
)

// Spawn modes for an agent step: how the engine treats an APIARY_SPAWN request
// emitted by the agent.
const (
	SpawnAuto  = "auto"  // default: fire-and-forget — do not block on the child
	SpawnAwait = "await" // block until the spawned task is terminal; child failure fails the step
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
	Priority int `yaml:"priority"`
	// Exclusive, when true, stops trigger evaluation after this trigger matches:
	// no lower-priority trigger is considered. Use it for a terminal classifier or
	// catch-all that must own a task alone rather than fan out alongside others.
	Exclusive bool       `yaml:"exclusive"`
	Match     RouteMatch `yaml:"match"`
}

// StepConfig is one node in a workflow graph. The active fields depend on Type.
type StepConfig struct {
	ID        string   `yaml:"id"`
	Type      string   `yaml:"type,omitempty"` // agent(default)|split|approval|foreach|workflow
	// DependsOn is set internally by the v2 lowering pass (parallel, foreach).
	// It cannot be declared in YAML — the engine uses implicit sequential ordering.
	DependsOn    []string `yaml:"-"`
	SeqDependsOn []string `yaml:"seq_depends_on,omitempty"`

	// ── shared / cross-cutting ────────────────────────────────────
	// Name is a human-readable label shown in logs and dashboards.
	Name string `yaml:"name,omitempty"`
	// Condition is a DAG IR expression (e.g. `memory.track == "implement"`).
	// When it evaluates to false the step is skipped (and its dependents cascade).
	// It is the lowered form of the v2 authored `if:` field.
	Condition string `yaml:"condition,omitempty"`
	// FailWhen is a DAG IR expression evaluated against the current memory plus
	// this step's fresh structured output after the agent runs. True → logical
	// rejection (treated as failure, eligible for on_fail.goto loop-back).
	// Lowered form of v2 `reject_when:`.
	FailWhen string `yaml:"fail_when,omitempty"`

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
	// Publish controls whether an APIARY_PUBLISH payload emitted by this step's
	// agent is written back to the task's source bindings: auto (default) | off.
	// Empty inherits the auto default.
	Publish string `yaml:"publish,omitempty"`
	// Spawn controls how an APIARY_SPAWN request emitted by this step's agent is
	// handled: auto (default, fire-and-forget) | await (block on the child).
	// Empty inherits the auto default.
	Spawn string `yaml:"spawn,omitempty"`

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

	// ── v2 authored fields (present before lowering; absent after) ───
	// These are written by humans in v2 syntax and lowered to the IR fields
	// above by LowerV2Workflow. They must not be used directly by the engine.

	// If is the authored guard expression (e.g. `${{ classify.track == 'complex' }}`).
	// Lowers to Condition.
	If string `yaml:"if,omitempty"`
	// RejectWhen is the authored rejection gate expression.
	// Lowers to FailWhen (with expression rewritten to use memory.* accessors).
	RejectWhen string `yaml:"reject_when,omitempty"`
	// OnReject is the authored loop-back spec. Lowers to OnFail.
	OnReject *OnRejectConfig `yaml:"on_reject,omitempty"`
	// SubSteps is a sequential group of child steps (v2 `steps:` inside a step).
	// Dissolved during lowering: children are inlined into the parent flat list.
	SubSteps []StepConfig `yaml:"steps,omitempty"`
	// ParallelSteps are concurrent child steps (v2 `parallel:` inside a step).
	// Lowers to a StepTypeParallel node with embedded children.
	ParallelSteps []StepConfig `yaml:"parallel,omitempty"`
	// Join is the parallel step join policy: all (default) | any | ${{ expr }}.
	Join string `yaml:"join,omitempty"`
	// ForEachExpr is the v2 `for_each:` expression (e.g. `${{ design.tasks }}`).
	// Lowers to Items (dot-path) + foreach step type.
	ForEachExpr string `yaml:"for_each,omitempty"`
	// Max is the v2 `max:` loop cap. Lowers to MaxItems.
	Max int `yaml:"max,omitempty"`
	// Output is the v2 alias for OutputSchema (shorter key in authored YAML).
	Output *OutputSchema `yaml:"output,omitempty"`
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

// OnRejectConfig is the v2 authored loop-back spec for reject_when gates.
// It lowers to StepOutcome (on_fail.goto + max_retries).
type OnRejectConfig struct {
	// RestartFrom names the earlier sibling step to restart from when rejected.
	RestartFrom string `yaml:"restart_from"`
	// Max is the maximum number of rejection+restart cycles (≥ 1).
	Max int `yaml:"max,omitempty"`
}

// IsV2Step reports whether this step was written in v2 authored form (i.e., it
// uses at least one v2-only field that must be lowered before execution).
func (s StepConfig) IsV2Step() bool {
	// A lowered parallel node carries its children in SubSteps but is already IR:
	// only the lowering pass sets Type=parallel, never the author. Treat it as
	// already-lowered so a second lowering pass exits early (LowerV2Workflow is
	// documented idempotent). Without this, len(SubSteps)>0 below would re-flag it
	// and lowerSteps would dissolve the parallel into a sequential group, losing
	// the concurrency and the join policy. (Lowered foreach already returns false:
	// it uses Items+Step, leaving SubSteps/ParallelSteps/ForEachExpr empty.)
	if s.Type == StepTypeParallel {
		return false
	}
	return s.If != "" || s.RejectWhen != "" || s.OnReject != nil ||
		len(s.SubSteps) > 0 || len(s.ParallelSteps) > 0 || s.ForEachExpr != "" || s.Output != nil
}
