package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	aplog "github.com/orlandoburli/apiary/internal/log"
)

type Config struct {
	Version       string           `yaml:"version"`
	Runners       []RunnerConfig   `yaml:"runners"`
	DefaultRunner string           `yaml:"default_runner"`
	Sources       []SourceConfig   `yaml:"sources"`
	Agents        []AgentConfig    `yaml:"agents"`
	Workers       []WorkerConfig   `yaml:"workers"`
	Workflows     []WorkflowConfig `yaml:"workflows"`
	Settings      Settings         `yaml:"settings"`
	Tasks         *TasksConfig     `yaml:"tasks"`

	rawContent string // original YAML text before env expansion; used by Save()
}

type SourceConfig struct {
	ID           string         `yaml:"id"`
	Type         string         `yaml:"type"`
	Config       map[string]any `yaml:"config"`
	PollInterval string         `yaml:"poll_interval"`
	Filters      SourceFilters  `yaml:"filters"`
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
	SourceToken string   `yaml:"source_token,omitempty"`
	SourceEmail string   `yaml:"source_email,omitempty"`
	SourceName  string   `yaml:"source_name,omitempty"`
	// Env is the agent-scope environment overlay applied to every step that runs
	// this agent, in any workflow. It is the lowest-precedence explicit env scope
	// (below workflow.env and step.env), layered on top of the identity overlay.
	Env map[string]string `yaml:"env,omitempty"`
	// Fallbacks is an ordered chain of alternative runner/model pairs to try when
	// the primary runner is rejected by a provider rate limit (e.g. Claude's
	// 5-hour session limit). The dispatcher pauses the rejected runner type until
	// it resets and retries the step on the next non-paused fallback. Empty means
	// no failover (the step fails / waits for the limit to reset).
	Fallbacks []FallbackConfig `yaml:"fallbacks,omitempty"`
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
	// AssignFromOutput parses the agent's output for an `APIARY-ASSIGN: <agent>`
	// directive and adds the corresponding `<prefix><agent>` label, so a
	// classifier agent can route a task to the agent it picked.
	AssignFromOutput bool `yaml:"assign_from_output"`
	// AssignLabelPrefix is the label prefix used for AssignFromOutput. Defaults
	// to "agent:" so the assigned label matches the agent:* route convention.
	AssignLabelPrefix string `yaml:"assign_label_prefix"`
}

// TasksConfig is the top-level `tasks:` block. Its hooks fire once per
// InternalTask — when the LAST of the task's fanned-out workflows reaches a
// terminal state — as opposed to the per-workflow on_complete/on_fail hooks
// which fire once per workflow instance. OnComplete applies when every instance
// succeeded; OnFail applies when any instance failed. Both follow the same rules
// as per-workflow hooks (set_state, add_labels; the removed assign_* directives
// are rejected by lint).
type TasksConfig struct {
	OnComplete *OnComplete `yaml:"on_complete"`
	OnFail     *OnComplete `yaml:"on_fail"`
}

type Settings struct {
	Concurrency   int    `yaml:"concurrency"`
	LogLevel      string `yaml:"log_level"`
	StateLock     bool   `yaml:"state_lock"`
	ResultComment bool   `yaml:"result_comment"`
	TaskTimeout   string `yaml:"task_timeout"`
	// MaxAttempts caps how many consecutive FAILED workflow instances a single
	// (task, workflow) pair may accumulate before the dispatcher stops
	// re-dispatching it and applies the workflow's on_fail hook. Rate-limited
	// runs are recorded as success (they fail over), so they do not count. This
	// is an internal backstop independent of source-side labels — it stops
	// runaway loops even for workflows with no on_fail. Default 3; <=0 disables.
	MaxAttempts int       `yaml:"max_attempts"`
	Telemetry   Telemetry `yaml:"telemetry"`
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
	if err := os.WriteFile(path, []byte(out), 0644); err != nil {
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

	if cfg.Settings.Concurrency == 0 {
		cfg.Settings.Concurrency = 2
	}
	if cfg.Settings.LogLevel == "" {
		cfg.Settings.LogLevel = "info"
	}
	if cfg.Settings.MaxAttempts == 0 {
		cfg.Settings.MaxAttempts = 3
	}
	warnDeprecatedResultComment(&cfg)
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
