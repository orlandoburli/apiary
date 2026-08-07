package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/plugin"
)

type Config struct {
	Version       string                              `yaml:"version"`
	Runners       []RunnerConfig                      `yaml:"runners"`
	DefaultRunner string                              `yaml:"default_runner"`
	Sources       []SourceConfig                      `yaml:"sources"`
	Agents        []AgentConfig                       `yaml:"agents"`
	Workers       []WorkerConfig                      `yaml:"workers"`
	Workflows     []WorkflowConfig                    `yaml:"workflows"`
	Profiles      map[string]map[string]ProfileConfig `yaml:"profiles,omitempty"` // NEW: named runner profiles
	Settings      Settings                            `yaml:"settings"`
	Tasks         *TasksConfig                        `yaml:"tasks"`
	Notifications *NotificationsConfig                `yaml:"notifications"`
	PluginDirs    []string                            `yaml:"plugin_dirs,omitempty"`
	Plugins       []plugin.InstanceConfig             `yaml:"plugins,omitempty"`

	rawContent string // original YAML text before env expansion; used by Save()
}

// ProfileConfig is an overlay that overrides runner, model, fallbacks, and
// fallback_strategy for a single agent when a profile is active.
type ProfileConfig struct {
	Runner           string           `yaml:"runner,omitempty"`
	Model            string           `yaml:"model,omitempty"`
	Fallbacks        []FallbackConfig `yaml:"fallbacks,omitempty"`
	FallbackStrategy string           `yaml:"fallback_strategy,omitempty"`
}

type SourceConfig struct {
	ID           string         `yaml:"id"`
	Type         string         `yaml:"type"`
	Config       map[string]any `yaml:"config"`
	PollInterval string         `yaml:"poll_interval"`
	Filters      SourceFilters  `yaml:"filters"`

	// InterruptOnResolve stops a still-running workflow instance when the
	// source item that triggered it is no longer active (an alert that stopped
	// firing). Off by default: the standing behaviour is to let an
	// investigation finish, because its findings usually outlive the alert.
	// Requires an adapter implementing source.ItemResolver.
	InterruptOnResolve bool `yaml:"interrupt_on_resolve"`
}

func (s SourceConfig) ParsedPollInterval() (time.Duration, error) {
	if s.PollInterval == "" {
		return 60 * time.Second, nil
	}
	return time.ParseDuration(s.PollInterval)
}

type SourceFilters struct {
	States []string `yaml:"states"`
	Labels []string `yaml:"labels"`
	JQL    string   `yaml:"jql"`
}

type RunnerConfig struct {
	ID       string         `yaml:"id"`
	Type     string         `yaml:"type"`               // cli | api — execution mode
	Provider string         `yaml:"provider,omitempty"` // adapter name: cli, opencode, script, claude, etc.
	Config   map[string]any `yaml:"config"`
	Models   []string       `yaml:"models,omitempty"`
	// MCPs are Model Context Protocol servers exposed to every agent that uses
	// this runner. Each CLI provider writes them into its own native MCP config
	// format/location (see runner/execution). Agent-scope MCPs (AgentConfig.MCPs)
	// are layered on top of these, overriding by name.
	MCPs []model.MCPServer `yaml:"mcps,omitempty"`
	// Sandbox, when set, wraps every agent subprocess for this runner in a Docker
	// container that isolates it from the host filesystem (only the task working
	// directory is mounted) and runs it as an unprivileged user. Use it for
	// runners that process issues/comments from external or untrusted authors, to
	// contain a successful prompt injection.
	Sandbox *SandboxConfig `yaml:"sandbox,omitempty"`
	// EnvPassthrough lists additional host environment variable names to forward to
	// the agent subprocess beyond the built-in allowlist (system vars + LLM/agent
	// provider credentials). Entries are exact names or a trailing-"*" prefix
	// (e.g. "MYCORP_*"). Host variables not covered by the allowlist or this list
	// are never inherited, so unrelated daemon secrets cannot leak into an agent.
	EnvPassthrough []string `yaml:"env_passthrough,omitempty"`
}

// SandboxConfig describes Docker container isolation for a CLI runner's agent
// subprocesses. Network is left enabled by default because coding agents must
// reach their LLM API and git remotes; set Network to "none" only for agents
// that need no network.
type SandboxConfig struct {
	// Image is the Docker image providing the agent binary (required).
	Image string `yaml:"image"`
	// User is passed as --user (e.g. "1000:1000"). Defaults to the daemon's own
	// uid:gid so the bind-mounted workspace stays writable — "nobody" would make
	// every workspace write fail with EACCES.
	User string `yaml:"user,omitempty"`
	// Network is the --network value (e.g. "bridge", "none"). Default "bridge".
	Network string `yaml:"network,omitempty"`
	// ExtraArgs are appended after the docker flags and before the image. Only
	// resource-limit and labelling flags are permitted (e.g.
	// ["--memory", "4g", "--pids-limit", "512"]); anything that could weaken the
	// sandbox — extra mounts, --privileged, --cap-add, --user, --network,
	// --read-only=false, --entrypoint — is rejected at config load.
	ExtraArgs []string `yaml:"extra_args,omitempty"`
}

// AdapterName returns the runner adapter name as "{provider}-{type}" when both
// are set, or falls back to Type alone (backward compat for bare names like "claude-cli").
func (r *RunnerConfig) AdapterName() string {
	if r.Provider != "" {
		return r.Provider + "-" + r.Type
	}
	return r.Type
}

type AgentConfig struct {
	ID          string   `yaml:"id"`
	Description string   `yaml:"description,omitempty"`
	SoulFile    string   `yaml:"soul_file,omitempty"`
	Model       string   `yaml:"model"`
	Skills      []string `yaml:"skills,omitempty"`
	Runner      string   `yaml:"runner,omitempty"`
	MaxWorkers  int      `yaml:"max_workers,omitempty"`
	// MaxTurns caps the number of agent turns per step run. 0 (the default)
	// means unlimited: CLI runners omit the provider's turns flag entirely, so
	// long-running coding tasks are never cut short unless explicitly capped.
	MaxTurns    int    `yaml:"max_turns,omitempty"`
	SourceToken string `yaml:"source_token,omitempty"`
	SourceEmail string `yaml:"source_email,omitempty"`
	SourceName  string `yaml:"source_name,omitempty"`
	// Env is the agent-scope environment overlay applied to every step that runs
	// this agent, in any workflow. It is the lowest-precedence explicit env scope
	// (below workflow.env and step.env), layered on top of the identity overlay.
	Env map[string]string `yaml:"env,omitempty"`
	// Fallbacks is an ordered chain of alternative runner/model pairs to try when
	// the primary runner is rejected by a provider rate limit or credit exhaustion
	// (e.g. Claude's 5-hour session limit, Codex out of credits). The dispatcher
	// pauses the rejected runner type until it resets and retries the step on the
	// next non-paused fallback. Empty means no failover (the step fails / waits
	// for the limit to reset).
	Fallbacks []FallbackConfig `yaml:"fallbacks,omitempty"`
	// FallbackStrategy selects the ordering policy for the candidate chain.
	// "ordered" (default) tries primary then fallbacks in config order.
	// "random" shuffles candidates.
	// "least_cost" sorts by historical average cost (ascending).
	// "fastest" sorts by historical average duration (ascending).
	FallbackStrategy string `yaml:"fallback_strategy,omitempty"`
	// MCPs are Model Context Protocol servers scoped to this agent. They are
	// layered on top of the runner's MCPs (RunnerConfig.MCPs), overriding any
	// runner-scope server with the same name.
	MCPs []model.MCPServer `yaml:"mcps,omitempty"`
	// Worker scheduling requirements are applied by the durable queue before a
	// workflow is delivered. Empty values accept any ready worker.
	WorkerPool           string   `yaml:"worker_pool,omitempty"`
	RequiresLabels       []string `yaml:"requires_labels,omitempty"`
	RequiresCapabilities []string `yaml:"requires_capabilities,omitempty"`
	// WorkspaceAffinity pins retries/resumes to the first worker that claims the
	// task, preserving a local checkout or other non-portable environment.
	WorkspaceAffinity bool `yaml:"workspace_affinity,omitempty"`
	// Permissions overrides the tool permissions written into the OpenCode agent
	// file. Keys are tool names ("bash", "edit", "webfetch", "read", "glob",
	// "grep", "task"); values are "allow" or "deny". Keys left out follow the
	// baseline: permissive by default, or deny for edit/bash/webfetch when
	// settings.least_privilege_agents is set. An explicit entry always wins, so a
	// single agent can be restricted without changing the global default —
	// e.g. permissions: {bash: deny} on a review-only agent.
	Permissions map[string]string `yaml:"permissions,omitempty"`
}

// FallbackConfig is one entry in an agent's rate-limit failover chain. Runner
// must reference a defined runner id; Model is optional (empty uses that
// runner's default model).
type FallbackConfig struct {
	Runner string `yaml:"runner"`
	Model  string `yaml:"model,omitempty"`
}

type WorkerConfig struct {
	ID          string          `yaml:"id"`
	Description string          `yaml:"description"`
	Runner      string          `yaml:"runner"`
	Model       string          `yaml:"model"`
	Config      WorkerRunConfig `yaml:"config"`
	// RunnerConfig holds runner-specific keys (e.g. command, model_flag for the
	// cli runner). These are passed directly to runner.Runner.Configure().
	RunnerConfig map[string]any `yaml:"runner_config"`
}

type WorkerRunConfig struct {
	WorkingDir   string            `yaml:"working_dir"`
	MaxTurns     int               `yaml:"max_turns"`
	SystemAppend string            `yaml:"system_prompt_append"`
	Env          map[string]string `yaml:"env"`
	Timeout      string            `yaml:"timeout"`
}

func (w WorkerRunConfig) ParsedTimeout() time.Duration {
	if w.Timeout == "" {
		return 30 * time.Minute
	}
	d, err := time.ParseDuration(w.Timeout)
	if err != nil {
		return 30 * time.Minute
	}
	return d
}

type RouteConfig struct {
	ID         string     `yaml:"id"`
	Priority   int        `yaml:"priority"`
	Match      RouteMatch `yaml:"match"`
	Agent      string     `yaml:"agent"`
	Worker     string     `yaml:"worker"`
	OnComplete OnComplete `yaml:"on_complete"`
	// Exclusive mirrors TriggerConfig.Exclusive: a matched exclusive route stops
	// RouteAll from considering any lower-priority route. Set by the Router when it
	// synthesizes routes from workflow triggers.
	Exclusive bool `yaml:"-"`
	// Once mirrors TriggerConfig.Once: the dispatcher drops this route on a re-poll
	// once the task already has a completed instance of it, so a run-at-most-once
	// workflow does not re-dispatch. Set by the Router from the workflow trigger.
	Once bool `yaml:"-"`
	// On mirrors TriggerConfig.On: the trigger's event axis ("" / "item" for
	// polled work items, or a pr_* event kind). Set by the Router from the
	// workflow trigger; event routes are evaluated by RouteEvent only.
	On string `yaml:"-"`
	// CommentMatches / Authors / AuthorsAssociation / MaxDispatches mirror the
	// same TriggerConfig fields for event routes. Set by the Router.
	CommentMatches     string   `yaml:"-"`
	Authors            []string `yaml:"-"`
	AuthorsAssociation []string `yaml:"-"`
	MaxDispatches      int      `yaml:"-"`
}

// IsEventRoute reports whether this route was synthesized from an event trigger
// (trigger.on: pr_*) rather than an item trigger.
func (r RouteConfig) IsEventRoute() bool {
	return r.On != "" && r.On != TriggerOnItem
}

type RouteMatch struct {
	Source     string   `yaml:"source"`
	Labels     []string `yaml:"labels"`
	Types      []string `yaml:"types"`
	TitleRegex string   `yaml:"title_regex"`
	Priority   []string `yaml:"priority"`
	// States, when set, restricts the route to cells whose state is in this
	// list (case-insensitive). Use it to gate a route to e.g. `todo` only.
	States []string `yaml:"states"`
	// ExcludeLabels rejects the cell if it carries ANY of these labels
	// (case-insensitive). The inverse of Labels.
	ExcludeLabels []string `yaml:"exclude_labels"`
	// ExcludeLabelPrefix rejects the cell if it carries any label starting with
	// this prefix (case-insensitive). E.g. "agent:" matches "no direct agent
	// assigned", so a classifier route only runs for unassigned cells.
	ExcludeLabelPrefix string `yaml:"exclude_label_prefix"`
}

type OnComplete struct {
	SetState  string   `yaml:"set_state"`
	AddLabels []string `yaml:"add_labels"`
	// RemoveLabels strips labels from the source item, so a label-driven trigger
	// (e.g. `labels: [create-spec]`) can clear its own trigger label on
	// completion instead of matching again on every poll. Removals are applied
	// after AddLabels; a label listed in both ends up removed.
	RemoveLabels []string `yaml:"remove_labels"`
	// AssignFromOutput parses the agent's output for an `APIARY-ASSIGN: <agent>`
	// directive and adds the corresponding `<prefix><agent>` label, so a
	// classifier agent can route a task to the agent it picked.
	AssignFromOutput bool `yaml:"assign_from_output"`
	// AssignLabelPrefix is the label prefix used for AssignFromOutput. Defaults
	// to "agent:" so the assigned label matches the agent:* route convention.
	AssignLabelPrefix string `yaml:"assign_label_prefix"`
}

// NotificationsConfig is the top-level `notifications:` block: it makes
// escalation visible to a human. Whenever a hook (a workflow's on_fail /
// on_complete, the task-level hooks, or the failure-cap park) adds one of
// OnLabels to a source item, every channel fires — so a flow parked with
// needs-attention pings an operator instead of freezing silently (#201).
type NotificationsConfig struct {
	// OnLabels are the escalation labels to watch (e.g. needs-attention).
	OnLabels []string `yaml:"on_labels"`
	// Channels are the notification hooks to fire, in order.
	Channels []NotificationChannel `yaml:"channels"`
}

// NotificationChannel is one notification hook. Only type "command" exists: an
// arbitrary shell command (ntfy, Slack webhook via curl, osascript, …) so the
// engine takes on no provider integrations. The command string may use
// {{task_id}}, {{cell_id}}, {{number}}, {{title}}, {{url}}, {{label}}, and
// {{summary}} placeholders (values are shell-quoted on substitution); the same
// values are also exported as APIARY_TASK_ID, APIARY_CELL_ID, APIARY_NUMBER,
// APIARY_TITLE, APIARY_URL, APIARY_LABEL, and APIARY_SUMMARY.
type NotificationChannel struct {
	Type string `yaml:"type"`
	Run  string `yaml:"run"`
}

// TasksConfig is the top-level `tasks:` block. Its hooks fire once per
// InternalTask — when the LAST of the task's fanned-out workflows reaches a
// terminal state — as opposed to the per-workflow on_complete/on_fail hooks
// which fire once per workflow instance. OnComplete applies when every instance
// succeeded; OnFail applies when any instance failed. Both follow the same rules
// as per-workflow hooks (set_state, add_labels, remove_labels; the removed
// assign_* directives are rejected by lint).
type TasksConfig struct {
	OnComplete *OnComplete `yaml:"on_complete"`
	OnFail     *OnComplete `yaml:"on_fail"`
}

type Settings struct {
	Concurrency int    `yaml:"concurrency"`
	LogLevel    string `yaml:"log_level"`
	StateLock   bool   `yaml:"state_lock"`
	// RefuseRoot makes running as root (euid 0) a startup error instead of a
	// warning. Agent CLIs inherit the daemon's uid, so a prompt-injected agent
	// executes with whatever privilege the daemon has. Default false — Apiary
	// warns but starts, so existing root service installs keep working; set true
	// to enforce a non-root daemon.
	RefuseRoot bool `yaml:"refuse_root,omitempty"`
	// LeastPrivilegeAgents makes agent tool permissions deny-by-default
	// (read/glob/grep/task allowed; edit/bash/webfetch denied) for runners that
	// support per-agent permissions. Default false preserves the historical
	// permissive behaviour; individual agents can always restrict themselves via
	// agents[].permissions regardless of this setting.
	LeastPrivilegeAgents bool   `yaml:"least_privilege_agents,omitempty"`
	ResultComment        bool   `yaml:"result_comment"`
	TaskTimeout          string `yaml:"task_timeout"`
	// MaxAttempts caps how many consecutive FAILED workflow instances a single
	// (task, workflow) pair may accumulate before the dispatcher stops
	// re-dispatching it and applies the workflow's on_fail hook. Rate-limited
	// runs are recorded as success (they fail over), so they do not count. This
	// is an internal backstop independent of source-side labels — it stops
	// runaway loops even for workflows with no on_fail. Default 3; <=0 disables.
	MaxAttempts int `yaml:"max_attempts"`
	// Log rotation for the shared apiary.log file and retention for rotated
	// backups and per-task log files. 0 means default; negative disables.
	LogMaxSizeMB  int                `yaml:"log_max_size_mb"`  // rotate apiary.log past this size; default 50
	LogMaxBackups int                `yaml:"log_max_backups"`  // rotated files to keep; default 5
	LogMaxAgeDays int                `yaml:"log_max_age_days"` // prune backups and task logs older than this; default 30
	Memory        MemorySettings     `yaml:"memory"`
	Events        EventSettings      `yaml:"events"`
	Approvals     ApprovalSettings   `yaml:"approvals"`
	Telemetry     Telemetry          `yaml:"telemetry"`
	CursorCost    CursorCostSettings `yaml:"cursor_cost"`
	Improve       ImproveSettings    `yaml:"improve"`
	GitHooks      GitHooksSettings   `yaml:"git_hooks"`
	Queue         QueueSettings      `yaml:"queue"`
	// DefaultFallbacks is a fallback chain applied to every agent that does not
	// define its own fallbacks[]. Entries must reference defined runner IDs.
	DefaultFallbacks []FallbackConfig `yaml:"default_fallbacks,omitempty"`
	// CreditExhaustedCooldown is how long a runner type is paused after a
	// credit-exhausted failure. Default "24h".
	CreditExhaustedCooldown string `yaml:"credit_exhausted_cooldown,omitempty"`
}

// QueueSettings controls durable dispatch and the embedded protocol-1 worker.
// Zero values preserve single-command local mode with safe defaults.
type QueueSettings struct {
	Enabled            *bool                    `yaml:"enabled,omitempty"`
	EmbeddedWorker     *bool                    `yaml:"embedded_worker,omitempty"`
	ProjectID          string                   `yaml:"project_id,omitempty"`
	WorkerID           string                   `yaml:"worker_id,omitempty"`
	WorkerPool         string                   `yaml:"worker_pool,omitempty"`
	WorkerLabels       []string                 `yaml:"worker_labels,omitempty"`
	WorkerCapabilities []string                 `yaml:"worker_capabilities,omitempty"`
	WorkerCapacity     int                      `yaml:"worker_capacity,omitempty"`
	LeaseDuration      string                   `yaml:"lease_duration,omitempty"`
	HeartbeatInterval  string                   `yaml:"heartbeat_interval,omitempty"`
	WorkerTimeout      string                   `yaml:"worker_timeout,omitempty"`
	PollInterval       string                   `yaml:"poll_interval,omitempty"`
	Listen             string                   `yaml:"listen,omitempty"`
	WorkerToken        string                   `yaml:"worker_token,omitempty"`
	Concurrency        QueueConcurrencySettings `yaml:"concurrency,omitempty"`
}

type QueueConcurrencySettings struct {
	DefaultProject int            `yaml:"default_project,omitempty"`
	Projects       map[string]int `yaml:"projects,omitempty"`
	DefaultSource  int            `yaml:"default_source,omitempty"`
	Sources        map[string]int `yaml:"sources,omitempty"`
	DefaultAgent   int            `yaml:"default_agent,omitempty"`
	Agents         map[string]int `yaml:"agents,omitempty"`
	DefaultRunner  int            `yaml:"default_runner,omitempty"`
	Runners        map[string]int `yaml:"runners,omitempty"`
	DefaultPool    int            `yaml:"default_pool,omitempty"`
	Pools          map[string]int `yaml:"pools,omitempty"`
}

func (q QueueSettings) IsEnabled() bool          { return q.Enabled == nil || *q.Enabled }
func (q QueueSettings) UsesEmbeddedWorker() bool { return q.EmbeddedWorker == nil || *q.EmbeddedWorker }

func (q QueueSettings) WorkerCapacityValue() int {
	if q.WorkerCapacity > 0 {
		return q.WorkerCapacity
	}
	return 1
}

func (q QueueSettings) LeaseDurationValue() time.Duration {
	return queueDuration(q.LeaseDuration, 30*time.Second)
}
func (q QueueSettings) HeartbeatIntervalValue() time.Duration {
	return queueDuration(q.HeartbeatInterval, 10*time.Second)
}
func (q QueueSettings) WorkerTimeoutValue() time.Duration {
	return queueDuration(q.WorkerTimeout, 30*time.Second)
}
func (q QueueSettings) PollIntervalValue() time.Duration {
	return queueDuration(q.PollInterval, 500*time.Millisecond)
}

func queueDuration(value string, fallback time.Duration) time.Duration {
	if value == "" {
		return fallback
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return fallback
	}
	return duration
}

// ApprovalSettings controls provider-neutral approval response endpoints.
type ApprovalSettings struct {
	WebhookSecret string   `yaml:"webhook_secret,omitempty"`
	RequireFor    []string `yaml:"require_for,omitempty"`
}

// EventSettings controls persisted structured execution events. Events are
// enabled whenever a database is configured; these fields govern retention and
// additional metadata keys that must be redacted before persistence/export.
type EventSettings struct {
	// Retention is a duration string. Empty defaults to 720h (30 days); zero or a
	// negative duration disables automatic pruning.
	Retention string `yaml:"retention"`
	// SensitiveFields extends the built-in credential-key denylist. Matching is
	// case-insensitive and ignores '-', '_', and '.'.
	SensitiveFields []string `yaml:"sensitive_fields"`
}

// RetentionDuration returns the configured event-retention window. Empty or
// invalid values use 720h; non-positive valid values disable pruning.
func (e EventSettings) RetentionDuration() time.Duration {
	if e.Retention == "" {
		return 720 * time.Hour
	}
	d, err := time.ParseDuration(e.Retention)
	if err != nil {
		return 720 * time.Hour
	}
	return d
}

// GitHooksSettings makes the daemon enforce a shared git hooks directory on
// the agents' local repo checkouts. At startup the daemon points
// core.hooksPath of every repo matched by Repos at Dir, so hooks placed there
// (e.g. a pre-push script that runs the project's lint/tests) physically gate
// what agents can push — prompt-level rules alone are advisory and get
// skipped under pressure. Hook scripts are made executable automatically.
type GitHooksSettings struct {
	// Dir is the directory holding the hook scripts (pre-push, pre-commit, …),
	// absolute, ~-prefixed, or relative to the daemon working directory.
	Dir string `yaml:"dir"`
	// Repos are glob patterns (absolute, ~-prefixed, or relative to the daemon
	// working directory) of git checkouts to enforce the hooks on, e.g.
	// "../project-erp--*". Non-git matches are skipped silently.
	Repos []string `yaml:"repos"`
}

// Enabled reports whether git hook enforcement is configured.
func (g GitHooksSettings) Enabled() bool {
	return g.Dir != "" && len(g.Repos) > 0
}

// CursorCostSettings configures the cursor-cli cost back-fill. The Cursor
// agent CLI reports token counts but no dollar cost in its stream output, so
// when enabled the daemon periodically queries Cursor's dashboard usage API
// (cookie auth) and back-fills cost_usd on finished cursor-cli executions by
// matching usage events to run time windows. Best-effort: the endpoint is
// undocumented, and overlapping concurrent cursor runs leave ambiguous events
// unattributed (cost becomes a lower bound). Disabled by default.
// ImproveSettings configures `apiary improve`, the self-improvement advisor.
// Everything here is optional: the command also accepts an advisor on the
// command line, and needs none at all for --dump-evidence.
type ImproveSettings struct {
	// Agent is the id of the agent that performs the analysis. It is resolved
	// like any other agent (runner, model, fallbacks, MCPs), so the advisor is
	// not a special case in the config. Empty falls back to an agent literally
	// named "improver", and failing that the command errors rather than guessing
	// — there is no default model to fall back to.
	Agent string `yaml:"agent,omitempty"`
	// EffortModels optionally maps an effort level (quick|standard|deep) to a
	// model, overriding the advisor agent's own model for that run. Reading
	// aggregate tables at "quick" and reasoning over transcripts and prose at
	// "deep" are different jobs; paying deep prices for a quick run is waste.
	// Unset levels fall through to the agent's model.
	EffortModels map[string]string `yaml:"effort_models,omitempty"`
}

type CursorCostSettings struct {
	Enabled bool `yaml:"enabled"`
	// SessionToken is the WorkosCursorSessionToken cookie from a logged-in
	// cursor.com browser session (valid ~60 days). Usually set as
	// "${CURSOR_SESSION_TOKEN}" resolved from the .env file beside apiary.yaml.
	// Empty falls back to the CURSOR_SESSION_TOKEN environment variable.
	SessionToken string `yaml:"session_token"`
	// Interval between back-fill sweeps (duration string). Default "5m".
	Interval string `yaml:"interval"`
	// MaxAge is how far back unpriced executions are considered (duration
	// string). Default "72h".
	MaxAge string `yaml:"max_age"`
	// TeamID scopes the dashboard query to a team account. Required when the
	// Cursor account bills through a team (the usual per-usage setup) — without
	// it the API silently returns no events. 0 means a personal account. Find it
	// in ~/.cursor/cli-config.json under authInfo.teamId.
	TeamID int `yaml:"team_id"`
	// UserID filters team queries to this member's events so teammates'
	// activity cannot pollute time-window attribution. Strongly recommended
	// whenever TeamID is set (authInfo.userId in the same file). 0 omits it.
	UserID int `yaml:"user_id"`
}

// IntervalDuration returns the parsed sweep interval (default 5m, floor 1m).
func (c CursorCostSettings) IntervalDuration() time.Duration {
	d, err := time.ParseDuration(c.Interval)
	if err != nil || d <= 0 {
		return 5 * time.Minute
	}
	if d < time.Minute {
		return time.Minute
	}
	return d
}

// MaxAgeDuration returns the parsed back-fill window (default 72h).
func (c CursorCostSettings) MaxAgeDuration() time.Duration {
	d, err := time.ParseDuration(c.MaxAge)
	if err != nil || d <= 0 {
		return 72 * time.Hour
	}
	return d
}

// MemorySettings configures the persistent agent memory store (the task and
// global tiers written via APIARY_MEMORIZE). Disabled by default: with
// Enabled false nothing is persisted or injected, and emitted markers are
// stripped from output and dropped.
type MemorySettings struct {
	Enabled bool `yaml:"enabled"`
	// Path is the memory root directory. Empty means <data-dir>/memory — the
	// .apiary/ folder beside the config file, next to apiary.db.
	Path string `yaml:"path"`
	// MaxInjectChars bounds the recall sections ([Long-term Memory] index +
	// [Task Memory] notes) injected into each step prompt. Default 4000.
	MaxInjectChars int `yaml:"max_inject_chars"`
	// MaxEntryBytes caps a single APIARY_MEMORIZE content. Default 16384.
	MaxEntryBytes int `yaml:"max_entry_bytes"`
	// TaskRetention is how long a task's notes are kept after the task reaches a
	// terminal state (duration string). Default "720h" (30 days).
	TaskRetention string `yaml:"task_retention"`
}

// TaskRetentionDuration returns the parsed retention window (default 720h).
func (m MemorySettings) TaskRetentionDuration() time.Duration {
	if m.TaskRetention == "" {
		return 720 * time.Hour
	}
	d, err := time.ParseDuration(m.TaskRetention)
	if err != nil {
		return 720 * time.Hour
	}
	return d
}

// CreditExhaustedCooldownDuration returns the parsed credit-exhausted cooldown
// duration (default 24h), floored at 1m.
func (s *Settings) CreditExhaustedCooldownDuration() time.Duration {
	d, err := time.ParseDuration(s.CreditExhaustedCooldown)
	if err != nil || d <= 0 {
		return 24 * time.Hour
	}
	if d < time.Minute {
		return time.Minute
	}
	return d
}

func (s *Settings) TaskTimeoutDuration() time.Duration {
	if s.TaskTimeout == "" {
		return 120 * time.Minute
	}
	d, err := time.ParseDuration(s.TaskTimeout)
	if err != nil {
		return 120 * time.Minute
	}
	return d
}

type Telemetry struct {
	Enabled  bool   `yaml:"enabled"`
	Endpoint string `yaml:"endpoint"`
}

// LoadDefaults returns a Config with sensible defaults.
func LoadDefaults() *Config {
	return &Config{
		Version: "1.0",
		Settings: Settings{
			Concurrency: 4,
			LogLevel:    "info",
		},
	}
}

// Save writes the config back to its YAML file, preserving original formatting,
// comments, and ${VAR} env references. It uses the raw content stored during
// Load() and surgically updates only the fields that changed in memory.
func (c *Config) Save(path string) error {
	out, err := c.mergeRawChanges()
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	return nil
}

// AgentDiff describes a set of changes to apply to an agent in the raw YAML.
type AgentDiff struct {
	ID         string
	Model      string // "" = no change
	Runner     string // "" = no change
	MaxWorkers int    // 0 = no change
}

// ApplyAgentDiff applies the diff to the in-memory struct and persists via Save.
// If path is empty, only the in-memory struct is updated (no file write).
func (c *Config) ApplyAgentDiff(path string, diff AgentDiff) error {
	for i := range c.Agents {
		if c.Agents[i].ID == diff.ID {
			if diff.Model != "" {
				c.Agents[i].Model = diff.Model
			}
			if diff.Runner != "" {
				c.Agents[i].Runner = diff.Runner
			}
			if diff.MaxWorkers > 0 {
				c.Agents[i].MaxWorkers = diff.MaxWorkers
			}
			break
		}
	}
	if path == "" {
		return nil
	}
	return c.Save(path)
}

// mergeRawChanges surgically applies in-memory agent changes to the raw YAML
// content using line-based manipulation, preserving original formatting,
// indentation, comments, key ordering, and ${VAR} references.
func (c *Config) mergeRawChanges() (string, error) {
	if c.rawContent == "" {
		data, err := yaml.Marshal(c)
		if err != nil {
			return "", fmt.Errorf("marshalling config: %w", err)
		}
		return string(data), nil
	}

	lines := strings.Split(c.rawContent, "\n")
	changed := false

	for _, memAgent := range c.Agents {
		idx := findAgentBlock(lines, memAgent.ID)
		if idx < 0 {
			continue
		}

		indent := leadingSpaces(lines[idx])

		if memAgent.Model != "" {
			if lineIdx, found := findKeyInBlock(lines, idx+1, indent, "model"); found {
				old := lines[lineIdx]
				updated := setYAMLValue(old, memAgent.Model)
				if updated != old {
					lines[lineIdx] = updated
					changed = true
				}
			} else {
				insertAt := idx + 1
				for insertAt < len(lines) && leadingSpaces(lines[insertAt]) > indent {
					insertAt++
				}
				kv := fmt.Sprintf("%s  model: %s", strings.Repeat(" ", indent), memAgent.Model)
				lines = append(lines[:insertAt], append([]string{kv}, lines[insertAt:]...)...)
				changed = true
			}
		}

		if memAgent.Runner != "" {
			idx2, found := findKeyInBlock(lines, idx+1, indent, "runner")
			if found {
				old := lines[idx2]
				new := setYAMLValue(old, memAgent.Runner)
				if new != old {
					lines[idx2] = new
					changed = true
				}
			} else {
				insertAt := idx + 1
				for insertAt < len(lines) && leadingSpaces(lines[insertAt]) > indent {
					insertAt++
				}
				kv := fmt.Sprintf("%s  runner: %s", strings.Repeat(" ", indent), memAgent.Runner)
				lines = append(lines[:insertAt], append([]string{kv}, lines[insertAt:]...)...)
				changed = true
			}
		}
		if memAgent.MaxWorkers > 0 {
			idx2, found := findKeyInBlock(lines, idx+1, indent, "max_workers")
			if found {
				old := lines[idx2]
				new := setYAMLValue(old, fmt.Sprintf("%d", memAgent.MaxWorkers))
				if new != old {
					lines[idx2] = new
					changed = true
				}
			} else {
				insertAt := idx + 1
				for insertAt < len(lines) && leadingSpaces(lines[insertAt]) > indent {
					insertAt++
				}
				kv := fmt.Sprintf("%s  max_workers: %d", strings.Repeat(" ", indent), memAgent.MaxWorkers)
				lines = append(lines[:insertAt], append([]string{kv}, lines[insertAt:]...)...)
				changed = true
			}
		}
	}

	if !changed {
		return c.rawContent, nil
	}
	return strings.Join(lines, "\n"), nil
}

// findAgentBlock finds the line index of "- id: <agentID>" in the YAML.
// Returns -1 if not found.
func findAgentBlock(lines []string, agentID string) int {
	target := "- id: " + agentID
	for i, line := range lines {
		if strings.TrimSpace(line) == target {
			return i
		}
	}
	return -1
}

// findKeyInBlock finds a line with the given key within a YAML block indented
// at blockIndent or deeper. Returns -1, false if not found.
func findKeyInBlock(lines []string, start int, blockIndent int, key string) (int, bool) {
	prefix := key + ":"
	for i := start; i < len(lines); i++ {
		sp := leadingSpaces(lines[i])
		if sp <= blockIndent && strings.TrimSpace(lines[i]) != "" {
			break // left the block
		}
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, prefix) && !strings.HasPrefix(trimmed, "#") {
			return i, true
		}
	}
	return -1, false
}

// setYAMLValue replaces the value part of a "key: value" YAML line.
func setYAMLValue(line, newValue string) string {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return line
	}
	return line[:idx+1] + " " + newValue
}

// listItemIndent returns the indentation of the first list item after a key line,
// or keyIndent+2 as a sensible default.
func listItemIndent(lines []string, keyLine int) int {
	for i := keyLine + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "-") {
			return leadingSpaces(lines[i])
		}
		break
	}
	return leadingSpaces(lines[keyLine]) + 2
}

// isListItem checks if a line is a YAML list item at the given indentation.
func isListItem(line string, indent int) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	return leadingSpaces(line) >= indent && (strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "-"))
}

// leadingSpaces returns the number of leading space characters.
func leadingSpaces(s string) int {
	for i, r := range s {
		if r != ' ' {
			return i
		}
	}
	return len(s)
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	// Auto-load .env from the config directory so env vars like
	// ${GITHUB_TOKEN_ENGINEER} resolve without manual sourcing.
	loadDotEnv(path)

	raw := string(data)
	expanded := expandEnv(raw)

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	cfg.rawContent = raw
	if err := resolveLocalWorkflows(path, &cfg); err != nil {
		return nil, err
	}

	if cfg.Settings.Concurrency == 0 {
		cfg.Settings.Concurrency = 2
	}
	if cfg.Settings.LogLevel == "" {
		cfg.Settings.LogLevel = "info"
	}
	if cfg.Settings.MaxAttempts == 0 {
		cfg.Settings.MaxAttempts = 3
	}
	if cfg.Settings.LogMaxSizeMB == 0 {
		cfg.Settings.LogMaxSizeMB = 50
	}
	if cfg.Settings.LogMaxBackups == 0 {
		cfg.Settings.LogMaxBackups = 5
	}
	if cfg.Settings.LogMaxAgeDays == 0 {
		cfg.Settings.LogMaxAgeDays = 30
	}
	if cfg.Settings.Memory.MaxInjectChars == 0 {
		cfg.Settings.Memory.MaxInjectChars = 4000
	}
	if cfg.Settings.Memory.MaxEntryBytes == 0 {
		cfg.Settings.Memory.MaxEntryBytes = 16384
	}
	warnDeprecatedResultComment(&cfg)
	warnUnenforceablePermissions(&cfg)
	return &cfg, nil
}

// warnDeprecatedResultComment logs a deprecation warning when result_comment is
// set to any non-default value, at the global settings level or on any workflow.
// The feature still works (see Engine.resultCommentMode); the APIARY_PUBLISH
// marker in agent output is the supported replacement.
func warnDeprecatedResultComment(cfg *Config) {
	const msg = "result_comment is deprecated; use APIARY_PUBLISH marker in agent output instead"
	if cfg.Settings.ResultComment {
		aplog.Warn("settings.result_comment: %s", msg)
	}
	for _, wf := range cfg.Workflows {
		if wf.ResultComment != "" {
			aplog.Warn("workflow %q: %s", wf.ID, msg)
		}
	}
}

// warnUnenforceablePermissions warns when per-agent tool permissions cannot be
// enforced for an agent's runner. Only the OpenCode runner writes an agent file
// carrying tool permissions; on other runners least_privilege_agents and
// agents[].permissions are silently inert, which would otherwise leave an
// operator believing a fleet is restricted when it is not.
func warnUnenforceablePermissions(cfg *Config) {
	adapters := map[string]string{}
	for i := range cfg.Runners {
		adapters[cfg.Runners[i].ID] = cfg.Runners[i].AdapterName()
	}
	for _, a := range cfg.Agents {
		runnerID := a.Runner
		if runnerID == "" {
			runnerID = cfg.DefaultRunner
		}
		adapter := adapters[runnerID]
		if adapter == "" || strings.HasPrefix(adapter, "opencode") {
			continue // unknown (reported elsewhere) or supported
		}
		if len(a.Permissions) > 0 {
			aplog.Warn("agents[%q]: permissions are ignored by runner %q (adapter %q); only the opencode runner enforces per-agent tool permissions", a.ID, runnerID, adapter)
		} else if cfg.Settings.LeastPrivilegeAgents {
			aplog.Warn("settings.least_privilege_agents is set but agent %q uses runner %q (adapter %q), which does not enforce per-agent tool permissions; this agent is NOT restricted", a.ID, runnerID, adapter)
		}
	}
}

func expandEnv(s string) string {
	return os.Expand(s, func(key string) string {
		// Leave workflow expression delimiters `${{ … }}` untouched — they are a
		// supported authoring syntax (see lower_v2.lowerExpr), not env references.
		// os.Expand splits `${{ expr }}` into key=`{ expr ` with the trailing `}`
		// left in the stream, so returning `${`+key+`}` reconstitutes the literal.
		if strings.HasPrefix(key, "{") {
			return "${" + key + "}"
		}
		return os.Getenv(strings.TrimSpace(key))
	})
}

// loadDotEnv reads .env from the same directory as the config file and calls
// os.Setenv for each entry. Lines starting with # are skipped.
func loadDotEnv(configPath string) {
	dir := filepath.Dir(configPath)
	envPath := filepath.Join(dir, ".env")
	data, err := os.ReadFile(envPath)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		if key != "" {
			// Don't override already-set env vars (e.g. from shell export)
			if os.Getenv(key) == "" {
				_ = os.Setenv(key, val)
			}
		}
	}
}
