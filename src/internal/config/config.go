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
	RetryPolicy   RetryPolicy `yaml:"retry_policy"`
	Telemetry     Telemetry   `yaml:"telemetry"`
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

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	expanded := expandEnv(string(data))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

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
