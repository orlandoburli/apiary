package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Version       string         `yaml:"version"`
	Runners       []RunnerConfig `yaml:"runners"`
	DefaultRunner string         `yaml:"default_runner"`
	Sources       []SourceConfig `yaml:"sources"`
	Agents        []AgentConfig  `yaml:"agents"`
	Workers       []WorkerConfig `yaml:"workers"`
	Routes        []RouteConfig  `yaml:"routes"`
	Settings      Settings       `yaml:"settings"`

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

func (r *RetryPolicy) ParsedBackoff() time.Duration {
	if r.parsedBackoff == 0 && r.BackoffBase != "" {
		d, _ := time.ParseDuration(r.BackoffBase)
		r.parsedBackoff = d
	}
	if r.parsedBackoff == 0 {
		return 1 * time.Second
	}
	return r.parsedBackoff
}

type SourceFilters struct {
	States []string `yaml:"states"`
	Labels []string `yaml:"labels"`
	JQL    string   `yaml:"jql"`
}

type RunnerConfig struct {
	ID     string         `yaml:"id"`
	Type   string         `yaml:"type"`
	Config map[string]any `yaml:"config"`
}

type AgentConfig struct {
	ID              string   `yaml:"id"`
	Description     string   `yaml:"description"`
	SoulFile        string   `yaml:"soul_file"`
	PreferredModels []string `yaml:"preferred_models"`
	Skills          []string `yaml:"skills"`
	Runner          string   `yaml:"runner"`
	MaxWorkers      int      `yaml:"max_workers"` // max concurrent tasks; 0 = default (1)
}

type WorkerConfig struct {
	ID          string          `yaml:"id"`
	Description string          `yaml:"description"`
	Runner      string          `yaml:"runner"`
	Model       string          `yaml:"model"`
	Config      WorkerRunConfig `yaml:"config"`
	// RunnerConfig holds runner-specific keys (e.g. command, model_flag for the
	// cli runner). These are passed directly to runner.Adapter.Configure().
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

type RetryPolicy struct {
	Enabled            bool     `yaml:"enabled"`
	MaxAttempts        int      `yaml:"max_attempts"`
	BackoffStrategy    string   `yaml:"backoff_strategy"` // "exponential" or "fixed"
	BackoffBase        string   `yaml:"backoff_base"`     // e.g., "1s", "5s"
	RetriableErrors    []string `yaml:"retriable_errors"`
	NonRetriableErrors []string `yaml:"non_retriable_errors"`
	parsedBackoff      time.Duration
}

type Settings struct {
	Concurrency   int         `yaml:"concurrency"`
	LogLevel      string      `yaml:"log_level"`
	StateLock     bool        `yaml:"state_lock"`
	ResultComment bool        `yaml:"result_comment"`
	TaskTimeout   string      `yaml:"task_timeout"`
	RetryPolicy   RetryPolicy `yaml:"retry_policy"`
	Telemetry     Telemetry   `yaml:"telemetry"`
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
			RetryPolicy: RetryPolicy{
				Enabled:         true,
				MaxAttempts:     3,
				BackoffStrategy: "exponential",
				BackoffBase:     "1s",
				RetriableErrors: []string{
					"timeout",
					"connection_error",
					"resource_unavailable",
					"rate_limited",
				},
				NonRetriableErrors: []string{
					"validation_error",
					"configuration_error",
					"not_found",
				},
			},
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
func (c *Config) ApplyAgentDiff(path string, diff AgentDiff) error {
	for i := range c.Agents {
		if c.Agents[i].ID == diff.ID {
			if diff.Model != "" {
				seen := map[string]bool{}
				var updated []string
				for _, m := range c.Agents[i].PreferredModels {
					seen[m] = true
					updated = append(updated, m)
				}
				if !seen[diff.Model] {
					updated = append([]string{diff.Model}, updated...)
				}
				c.Agents[i].PreferredModels = updated
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
	return c.Save(path)
}

// mergeRawChanges surgically applies in-memory agent changes to the raw YAML
// content, preserving comments, formatting, and ${VAR} references.
func (c *Config) mergeRawChanges() (string, error) {
	if c.rawContent == "" {
		// No raw content available — fall back to plain marshal.
		data, err := yaml.Marshal(c)
		if err != nil {
			return "", fmt.Errorf("marshalling config: %w", err)
		}
		return string(data), nil
	}

	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(c.rawContent), &doc); err != nil {
		return "", fmt.Errorf("parsing raw config: %w", err)
	}

	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return c.rawContent, nil
	}

	root := doc.Content[0] // the mapping node
	if root.Kind != yaml.MappingNode {
		return c.rawContent, nil
	}

	// Find the agents key in the root mapping
	var agentsNode *yaml.Node
	for i := 0; i < len(root.Content)-1; i += 2 {
		if root.Content[i].Value == "agents" {
			agentsNode = root.Content[i+1]
			break
		}
	}
	if agentsNode == nil || agentsNode.Kind != yaml.SequenceNode {
		// No agents section — fall back to plain marshal
		data, err := yaml.Marshal(c)
		if err != nil {
			return "", fmt.Errorf("marshalling config: %w", err)
		}
		return string(data), nil
	}

	// For each agent in memory, find the matching node in the YAML tree
	// and update its scalar values.
	for _, memAgent := range c.Agents {
		node := findAgentNode(agentsNode, memAgent.ID)
		if node == nil {
			continue
		}
		updateAgentNode(node, memAgent)
	}

	data, err := yaml.Marshal(&doc)
	if err != nil {
		return "", fmt.Errorf("marshalling updated config: %w", err)
	}
	return string(data), nil
}

// findAgentNode searches a YAML sequence of agent mappings for one with
// matching id. Returns the mapping node or nil.
func findAgentNode(seq *yaml.Node, id string) *yaml.Node {
	for _, item := range seq.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j < len(item.Content)-1; j += 2 {
			if item.Content[j].Value == "id" && item.Content[j+1].Value == id {
				return item
			}
		}
	}
	return nil
}

// updateAgentNode sets scalar fields in the YAML mapping node to match the
// in-memory agent config. Non-scalar fields (preferred_models, skills) are
// skipped — the raw content preserves them unchanged from Load().
func updateAgentNode(node *yaml.Node, agent AgentConfig) {
	setScalar(node, "runner", agent.Runner)
	setScalar(node, "max_workers", intOrString(agent.MaxWorkers))
}

// setScalar sets the value of a key in a mapping node. If the key doesn't
// exist it is appended. Value is set only if non-empty / non-zero.
func setScalar(node *yaml.Node, key, value string) {
	if value == "" || value == "0" {
		return
	}
	for i := 0; i < len(node.Content)-1; i += 2 {
		if node.Content[i].Value == key {
			node.Content[i+1].Value = value
			return
		}
	}
	// Key not found — append it.
	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Value: value},
	)
}

func intOrString(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%d", n)
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

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
	// Fill in retry policy defaults if not specified
	if cfg.Settings.RetryPolicy.MaxAttempts == 0 {
		cfg.Settings.RetryPolicy.MaxAttempts = 3
	}
	if cfg.Settings.RetryPolicy.BackoffStrategy == "" {
		cfg.Settings.RetryPolicy.BackoffStrategy = "exponential"
	}
	if cfg.Settings.RetryPolicy.BackoffBase == "" {
		cfg.Settings.RetryPolicy.BackoffBase = "1s"
	}
	if !cfg.Settings.RetryPolicy.Enabled && cfg.Settings.RetryPolicy.MaxAttempts > 0 {
		cfg.Settings.RetryPolicy.Enabled = true
	}

	return &cfg, nil
}

func expandEnv(s string) string {
	return os.Expand(s, func(key string) string {
		return os.Getenv(strings.TrimSpace(key))
	})
}
