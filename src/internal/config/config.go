package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Version  string         `yaml:"version"`
	Sources  []SourceConfig `yaml:"sources"`
	Workers  []WorkerConfig `yaml:"workers"`
	Routes   []RouteConfig  `yaml:"routes"`
	Settings Settings       `yaml:"settings"`
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

type WorkerConfig struct {
	ID           string            `yaml:"id"`
	Description  string            `yaml:"description"`
	Runner       string            `yaml:"runner"`
	Model        string            `yaml:"model"`
	Config       WorkerRunConfig   `yaml:"config"`
	// RunnerConfig holds runner-specific keys (e.g. command, model_flag for the
	// cli runner). These are passed directly to runner.Adapter.Configure().
	RunnerConfig map[string]any    `yaml:"runner_config"`
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
	ID         string      `yaml:"id"`
	Priority   int         `yaml:"priority"`
	Match      RouteMatch  `yaml:"match"`
	Worker     string      `yaml:"worker"`
	OnComplete OnComplete  `yaml:"on_complete"`
}

type RouteMatch struct {
	Source     string   `yaml:"source"`
	Labels     []string `yaml:"labels"`
	Types      []string `yaml:"types"`
	TitleRegex string   `yaml:"title_regex"`
	Priority   []string `yaml:"priority"`
}

type OnComplete struct {
	SetState  string   `yaml:"set_state"`
	AddLabels []string `yaml:"add_labels"`
}

type Settings struct {
	Concurrency   int         `yaml:"concurrency"`
	LogLevel      string      `yaml:"log_level"`
	StateLock     bool        `yaml:"state_lock"`
	ResultComment bool        `yaml:"result_comment"`
	Telemetry     Telemetry   `yaml:"telemetry"`
}

type Telemetry struct {
	Enabled  bool   `yaml:"enabled"`
	Endpoint string `yaml:"endpoint"`
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

	return &cfg, nil
}

func expandEnv(s string) string {
	return os.Expand(s, func(key string) string {
		return os.Getenv(strings.TrimSpace(key))
	})
}
