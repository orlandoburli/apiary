package config

import "time"

// Step type identifiers. A step with an empty Type is treated as StepTypeAgent.
const (
	StepTypeAgent    = "agent"
	StepTypeSplit    = "split"
	StepTypeApproval = "approval"
	StepTypeForeach  = "foreach"
	StepTypeWorkflow = "workflow"
	StepTypeWaitFor  = "wait_for"
	// StepTypeParallel is emitted by the v2 lowering pass for `parallel:` steps.
	// Children run concurrently (§8e); the step's outcome = the join policy.
	StepTypeParallel = "parallel"
)

// Join policy values for a parallel step. Any other non-empty value is a
// condition expression (optionally ${{ }}-wrapped) evaluated over the
// children's outcomes — see workflow.applyJoinPolicy.
const (
	JoinAll = "all" // default: every child must pass
	JoinAny = "any" // at least one child must pass
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
	ResultCommentOnFail     = "on_fail" // post only when the workflow fails
	ResultCommentAlways     = "always"  // post on both success and failure
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

// Materialize modes for an agent step: whether each APIARY_SPAWN child is
// published to the source as a sub-issue under the parent's source item.
const (
	MaterializeOff      = ""          // default: spawned children stay internal
	MaterializeSubIssue = "sub_issue" // create one source sub-issue per spawned child
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
	// Inputs and Outputs form the public contract of a reusable workflow. They
	// are optional for top-level workflows and for legacy same-file references.
	Inputs  map[string]WorkflowInput  `yaml:"inputs,omitempty"`
	Outputs map[string]WorkflowOutput `yaml:"outputs,omitempty"`
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
	// Env is the workflow-scope environment overlay applied to every step of this
	// workflow. It overrides agent.env and is overridden by step.env.
	Env map[string]string `yaml:"env,omitempty"`
}

// ResumePolicy returns the effective resume policy, defaulting to ResumeAllowed.
func (w WorkflowConfig) ResumePolicy() string {
	if w.Resume == "" {
		return ResumeAllowed
	}
	return w.Resume
}

// Trigger event kinds for TriggerConfig.On. The default (empty) is "item":
// today's behavior, where polled work items are matched against the trigger.
// The pr_* kinds instead match pull-request events polled from sources that
// implement PREventPoller.
const (
	TriggerOnItem                   = "item"
	TriggerOnPRComment              = "pr_comment"
	TriggerOnPRReviewApproved       = "pr_review_approved"
	TriggerOnPRReviewChangesRequest = "pr_review_changes_requested"
)

// TriggerEventKinds lists the PR event kinds accepted in trigger.on (everything
// except the implicit default "item").
var TriggerEventKinds = []string{
	TriggerOnPRComment,
	TriggerOnPRReviewApproved,
	TriggerOnPRReviewChangesRequest,
}

// validAuthorAssociations are the source author-association values accepted in
// trigger.authors_association (GitHub's vocabulary; other forges map onto it).
var validAuthorAssociations = map[string]bool{
	"OWNER": true, "MEMBER": true, "COLLABORATOR": true,
	"CONTRIBUTOR": true, "FIRST_TIME_CONTRIBUTOR": true, "FIRST_TIMER": true,
	"MANNEQUIN": true, "NONE": true,
}

// DefaultEventAuthorAssociations is the default actor gate for event triggers
// when neither authors nor authors_association is declared: collaborators-only,
// so drive-by comments from strangers cannot spawn agents. This is a security
// boundary, not a convenience.
var DefaultEventAuthorAssociations = []string{"OWNER", "MEMBER", "COLLABORATOR"}

// TriggerConfig selects which tasks start a workflow. It mirrors route matching:
// priority ordering plus a RouteMatch condition.
type TriggerConfig struct {
	Priority int `yaml:"priority"`
	// Exclusive, when true, stops trigger evaluation after this trigger matches:
	// no lower-priority trigger is considered. Use it for a terminal classifier or
	// catch-all that must own a task alone rather than fan out alongside others.
	//
	// The claim holds even when a pre-dispatch guard later drops this workflow
	// (live instance, spent `once`, failure cap): the suppressed triggers are not
	// reconsidered, since running them then would duplicate the work this trigger
	// exists to own. The daemon names them in its fully-dropped INFO report.
	Exclusive bool `yaml:"exclusive"`
	// Once, when true, makes the workflow run at most once per task: once it has a
	// completed (done) instance for the task, later polls do not re-dispatch it even
	// if the task still matches the trigger. Use it for a decomposition/fan-out
	// workflow whose source item (e.g. a spec issue) stays in its trigger set after
	// the workflow succeeds — without it, every poll re-dispatches and produces a
	// duplicate set of children (issue #119). Failed runs are not blocked (they
	// remain eligible for retry up to settings.max_attempts).
	Once  bool       `yaml:"once"`
	Match RouteMatch `yaml:"match"`

	// On selects the trigger's event axis: "item" (default — polled work items)
	// or a PR event kind (pr_comment, pr_review_approved,
	// pr_review_changes_requested) polled from a PREventPoller-capable source.
	On string `yaml:"on,omitempty"`
	// CommentMatches, valid only with on: pr_comment, requires the comment body
	// to match this Go regexp — the title_regex convention (case-sensitive; use
	// (?i) for case-insensitive). E.g. "(?i)@apiary\\s+(fix|update)". Compiled
	// at config load; an invalid pattern fails validation.
	CommentMatches string `yaml:"comment_matches,omitempty"`
	// Authors, when set, restricts an event trigger to events authored by one of
	// these source logins (case-insensitive). Takes precedence over
	// AuthorsAssociation.
	Authors []string `yaml:"authors,omitempty"`
	// AuthorsAssociation restricts an event trigger to authors whose repository
	// association is in this list (e.g. OWNER, MEMBER, COLLABORATOR). When both
	// this and Authors are empty the default is collaborators-only
	// (DefaultEventAuthorAssociations).
	AuthorsAssociation []string `yaml:"authors_association,omitempty"`
	// MaxDispatches caps how many times this trigger may dispatch for the same
	// pull request (a runaway-loop budget, analogous to on_conflict's own
	// budget). 0 means unlimited.
	MaxDispatches int `yaml:"max_dispatches,omitempty"`
}

// EventKind returns the trigger's event axis, defaulting to TriggerOnItem.
func (t TriggerConfig) EventKind() string {
	if t.On == "" {
		return TriggerOnItem
	}
	return t.On
}

// IsEventTrigger reports whether this trigger fires on PR events rather than
// polled work items.
func (t TriggerConfig) IsEventTrigger() bool {
	return t.EventKind() != TriggerOnItem
}

// StepConfig is one node in a workflow graph. The active fields depend on Type.
type StepConfig struct {
	ID   string `yaml:"id"`
	Type string `yaml:"type,omitempty"` // agent(default)|split|approval|foreach|workflow
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
	// OnConflict is the merge-conflict edge of a wait_for/ci step (goto +
	// max_retries, same shape as on_fail). When the step fails because the PR has
	// merge conflicts, this route governs the loop-back exclusively, with its own
	// retry budget separate from on_fail. Absent → a conflict falls through to
	// on_fail like any other failure.
	OnConflict *StepOutcome `yaml:"on_conflict,omitempty"`
	// Publish controls whether an APIARY_PUBLISH payload emitted by this step's
	// agent is written back to the task's source bindings: auto (default) | off.
	// Empty inherits the auto default.
	Publish string `yaml:"publish,omitempty"`
	// Spawn controls how an APIARY_SPAWN request emitted by this step's agent is
	// handled: auto (default, fire-and-forget) | await (block on the child).
	// Empty inherits the auto default.
	Spawn string `yaml:"spawn,omitempty"`
	// Materialize controls whether each child created from this step's APIARY_SPAWN
	// is published to the source as a sub-issue under the parent's source item:
	// "" (default, off) | sub_issue. With sub_issue, the engine creates one source
	// sub-issue per spawned child exactly once (guarded by the child dedup key and
	// the source_bindings unique constraint), so re-running a decomposition agent
	// never produces a duplicate set of sub-issues (issue #119). The created
	// sub-issue carries the spawn request's labels, so the normal poll→route loop
	// picks it up and dispatches the matching workflow.
	Materialize string `yaml:"materialize,omitempty"`
	// PullRequestFrom names a field of this step's structured output holding the
	// URL of a pull request the step opened (e.g. `pull_request_from: pr_url`).
	// The engine parses that URL and links the PR to the task, which is what the
	// dashboard's "open PR" shortcut reads.
	//
	// It exists because PR discovery is otherwise a GitHub-source privilege: the
	// PRs of a task sourced from Jira or Plane are invisible, since those
	// adapters cannot enumerate them. Declaring the field the agent already
	// emits makes the link work regardless of source type (#425).
	PullRequestFrom string `yaml:"pull_request_from,omitempty"`
	// Env is the step-scope environment overlay. It is the highest-precedence
	// explicit scope: it overrides workflow.env and agent.env for the same key.
	Env map[string]string `yaml:"env,omitempty"`
	// ActionClass lets settings.approvals.require_for enforce an approval gate
	// before sensitive work such as push, deploy, destructive, or publication.
	ActionClass string `yaml:"action_class,omitempty"`

	// ── split step ────────────────────────────────────────────────
	Multi    bool          `yaml:"multi,omitempty"`
	Branches []SplitBranch `yaml:"branches,omitempty"`

	// ── approval step ─────────────────────────────────────────────
	Message           string              `yaml:"message,omitempty"`
	ResumeOn          *ApprovalTrigger    `yaml:"resume_on,omitempty"`
	AbortOn           *ApprovalTrigger    `yaml:"abort_on,omitempty"`
	Timeout           string              `yaml:"timeout,omitempty"`
	Approvers         []string            `yaml:"approvers,omitempty"`
	RequiredApprovals int                 `yaml:"required_approvals,omitempty"`
	ApprovalFields    []ApprovalField     `yaml:"fields,omitempty"`
	RemindAfter       string              `yaml:"remind_after,omitempty"`
	EscalateAfter     string              `yaml:"escalate_after,omitempty"`
	EscalateTo        []string            `yaml:"escalate_to,omitempty"`
	Delegates         map[string][]string `yaml:"delegates,omitempty"`

	// ── wait_for step ─────────────────────────────────────────────
	// WaitFor holds configuration for a wait_for step (e.g., wait for CI). The
	// step suspends the workflow until the awaited condition resolves.
	WaitFor *WaitForConfig `yaml:"wait_for,omitempty"`

	// ── foreach step ──────────────────────────────────────────────
	Items       string      `yaml:"items,omitempty"`
	As          string      `yaml:"as,omitempty"`
	Concurrency int         `yaml:"concurrency,omitempty"`
	MaxItems    int         `yaml:"max_items,omitempty"`
	FailFast    bool        `yaml:"fail_fast,omitempty"`
	Step        *StepConfig `yaml:"step,omitempty"`

	// ── sub-workflow step ─────────────────────────────────────────
	// Workflow is the resolved child workflow ID used by the engine. It remains
	// authorable for backwards-compatible same-file references.
	Workflow string `yaml:"workflow,omitempty"`
	// Uses references a reusable workflow file relative to the declaring YAML.
	// Config.Load resolves it into Workflow before validation/execution.
	Uses string `yaml:"uses,omitempty"`
	// With binds values to the reusable workflow's declared inputs.
	With map[string]any `yaml:"with,omitempty"`

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

// ApprovalField describes one typed value collected with a response.
type ApprovalField struct {
	Name     string   `yaml:"name" json:"name"`
	Label    string   `yaml:"label,omitempty" json:"label,omitempty"`
	Type     string   `yaml:"type,omitempty" json:"type,omitempty"`
	Required bool     `yaml:"required,omitempty" json:"required,omitempty"`
	Options  []string `yaml:"options,omitempty" json:"options,omitempty"`
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

// MemorizeEnabled reports whether APIARY_MEMORIZE requests emitted by this
// step's agent should be persisted. Defaults to true; only an explicit
// `memory.memorize: off` disables it.
func (s StepConfig) MemorizeEnabled() bool {
	return s.Memory == nil || s.Memory.Memorize != MemorizeOff
}

// MemoryRecallTiers returns the persistent memory tiers to inject into this
// step's prompt. An unset/empty `memory.recall` means both tiers.
func (s StepConfig) MemoryRecallTiers() []string {
	if s.Memory == nil || len(s.Memory.Recall) == 0 {
		return []string{MemoryTierTask, MemoryTierGlobal}
	}
	return s.Memory.Recall
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

// Memorize handling for a step's APIARY_MEMORIZE requests.
const (
	MemorizeAuto = "auto" // default: persist requests the agent emits
	MemorizeOff  = "off"  // drop requests, even if the agent emits them
)

// Persistent memory tiers a step may recall (step.memory.recall values).
const (
	MemoryTierTask   = "task"
	MemoryTierGlobal = "global"
)

// MemoryConfig controls how a step interacts with the workflow memory object
// (the per-instance document) and the persistent memory store (the task and
// global tiers).
type MemoryConfig struct {
	// Read is a pointer so an explicit `read: false` is distinguishable from an
	// unset value (which defaults to true). Use StepConfig.MemoryReadEnabled().
	// It gates the entire memory document, persistent recall sections included.
	Read  *bool    `yaml:"read,omitempty"`
	Write []string `yaml:"write,omitempty"`
	// Recall lists the persistent tiers injected into this step's prompt: any
	// subset of "task" and "global". Empty means both. Use
	// StepConfig.MemoryRecallTiers().
	Recall []string `yaml:"recall,omitempty"`
	// Memorize controls the step's APIARY_MEMORIZE handling: auto (default) | off.
	Memorize string `yaml:"memorize,omitempty"`
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

// wait_for kinds: what external condition a wait_for step polls.
const (
	// WaitKindCI waits for the CI status of the PR linked to the task.
	WaitKindCI = "ci"
	// WaitKindDependency waits until every upstream blocker of the task (e.g. a
	// Jira "is blocked by" link) is satisfied — merged and/or Done per
	// satisfied_when — then auto-resumes the workflow.
	WaitKindDependency = "dependency"
)

// on_timeout actions for a wait_for step whose max_duration elapses.
const (
	// OnTimeoutFail fails the step at the deadline (the CI default).
	OnTimeoutFail = "fail"
	// OnTimeoutHold keeps the instance parked past the deadline, leaving it for a
	// human to resolve (the dependency default).
	OnTimeoutHold = "hold"
)

// blocker satisfaction conditions accepted in satisfied_when.
const (
	// BlockerSatisfiedMerged accepts a blocker whose linked PR is merged.
	BlockerSatisfiedMerged = "merged"
	// BlockerSatisfiedDone accepts a blocker whose status is Done-category
	// (resolved/closed).
	BlockerSatisfiedDone = "done"
)

// WaitForConfig configures a wait_for step that suspends the workflow until an
// external condition (e.g. CI status) resolves, re-checking each poll cycle until
// the condition is met or a timeout occurs.
type WaitForConfig struct {
	// Kind specifies what to poll: "ci" (default) for the CI status of the task's
	// PR, or "dependency" for the task's upstream blockers (auto-resumes once
	// every blocker is merged/Done).
	Kind string `yaml:"kind,omitempty"`
	// CheckInterval is how often to query the status (e.g., "30s"). Defaults to 1m.
	CheckInterval string `yaml:"check_interval,omitempty"`
	// MaxDuration is the total timeout for polling (e.g., "2h"). Defaults to 2h
	// for kind: ci and to no deadline for kind: dependency (a blocker may take
	// days to land).
	MaxDuration string `yaml:"max_duration,omitempty"`
	// FailIfNotPassed, when true, rejects the step if CI is not green. Defaults to true.
	FailIfNotPassed *bool `yaml:"fail_if_not_passed,omitempty"`
	// RemoveLabel, when set, removes this label from the task before polling begins.
	// Used to reset stale labels from previous runs.
	RemoveLabel string `yaml:"remove_label,omitempty"`

	// ── kind: dependency only ─────────────────────────────────────
	// SatisfiedWhen lists the conditions under which a blocker counts as
	// satisfied: "merged" (a linked PR merged) and/or "done" (status is
	// Done-category). A blocker is satisfied when ANY listed condition holds.
	// Defaults to [merged, done].
	SatisfiedWhen []string `yaml:"satisfied_when,omitempty"`
	// BlockerLinkType is the source-native relation that marks a blocker (e.g.
	// Jira's "Blocks" link type, read from its inward "is blocked by" side).
	// Empty uses the source's default blocking relation.
	BlockerLinkType string `yaml:"blocker_link_type,omitempty"`
	// OnTimeout selects what happens when max_duration elapses: "fail" fails the
	// step, "hold" keeps the instance parked for a human. Defaults to fail for
	// kind: ci and hold for kind: dependency.
	OnTimeout string `yaml:"on_timeout,omitempty"`
}

// ParsedCheckInterval returns the check interval duration, defaulting to 1 minute.
func (p *WaitForConfig) ParsedCheckInterval() time.Duration {
	if p == nil || p.CheckInterval == "" {
		return time.Minute
	}
	d, _ := time.ParseDuration(p.CheckInterval)
	if d <= 0 {
		return time.Minute
	}
	return d
}

// ParsedMaxDuration returns the maximum polling duration. Unset/invalid values
// default to 2 hours for kind: ci and to 0 (no deadline) for kind: dependency —
// a blocker may legitimately take days, and the dependency default on_timeout
// is hold anyway.
func (p *WaitForConfig) ParsedMaxDuration() time.Duration {
	fallback := 2 * time.Hour
	if p != nil && p.Kind == WaitKindDependency {
		fallback = 0
	}
	if p == nil || p.MaxDuration == "" {
		return fallback
	}
	d, _ := time.ParseDuration(p.MaxDuration)
	if d <= 0 {
		return fallback
	}
	return d
}

// ShouldFailIfNotPassed returns whether to reject the step on non-green CI, defaulting to true.
func (p *WaitForConfig) ShouldFailIfNotPassed() bool {
	if p == nil || p.FailIfNotPassed == nil {
		return true
	}
	return *p.FailIfNotPassed
}

// EffectiveSatisfiedWhen returns the blocker satisfaction conditions, defaulting
// to [merged, done] — a blocker counts as satisfied when any condition holds.
func (p *WaitForConfig) EffectiveSatisfiedWhen() []string {
	if p == nil || len(p.SatisfiedWhen) == 0 {
		return []string{BlockerSatisfiedMerged, BlockerSatisfiedDone}
	}
	return p.SatisfiedWhen
}

// TimeoutAction returns what happens when max_duration elapses, defaulting to
// fail for kind: ci and hold for kind: dependency.
func (p *WaitForConfig) TimeoutAction() string {
	if p != nil && p.OnTimeout != "" {
		return p.OnTimeout
	}
	if p != nil && p.Kind == WaitKindDependency {
		return OnTimeoutHold
	}
	return OnTimeoutFail
}

// OutputSchema is the supported JSON Schema subset for structured step output.
type OutputSchema struct {
	Type       string                 `yaml:"type"`
	Properties map[string]SchemaField `yaml:"properties,omitempty"`
	Required   []string               `yaml:"required,omitempty"`
}

// WorkflowInput declares one typed value accepted by a reusable workflow.
type WorkflowInput struct {
	Type     string `yaml:"type"`
	Required bool   `yaml:"required,omitempty"`
	Default  any    `yaml:"default,omitempty"`
}

// WorkflowOutput maps one typed public result to a child step output.
type WorkflowOutput struct {
	Type  string `yaml:"type"`
	Value string `yaml:"value"`
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
