package improve

import (
	"fmt"
	"sort"
	"strings"

	"github.com/orlandoburli/apiary/internal/config"
)

// DefaultAdvisorAgentID is the agent id looked up when nothing else names an
// advisor. Defining an agent with this id is enough to make `apiary improve`
// work with no further configuration.
const DefaultAdvisorAgentID = "improver"

// Advisor is the resolved identity that will perform the analysis: which runner
// adapter to instantiate, with what configuration, on which model.
type Advisor struct {
	AgentID  string
	RunnerID string
	Model    string
	MaxTurns int

	// Runner is the resolved runner block. Nil only for an ad-hoc --runner that
	// names a runner absent from config, which ResolveAdvisor rejects.
	Runner *config.RunnerConfig
	// Agent is the resolved agent block, or nil for an ad-hoc --runner/--model
	// pair with no corresponding agent.
	Agent *config.AgentConfig
	// Fallbacks is the chain to try when the primary runner is rejected, already
	// resolved from the agent's own chain or the global default.
	Fallbacks []config.FallbackConfig
	// Source records how the advisor was chosen, for the "analysing with …" line.
	Source string
}

// AdvisorFlags carries the command-line overrides that participate in
// resolution.
type AdvisorFlags struct {
	Advisor string
	Runner  string
	Model   string
	Effort  Effort
}

// ResolveAdvisor decides which agent performs the analysis, in this order:
//
//  1. --advisor <agent-id>
//  2. --runner <id> --model <name>   (ad-hoc, no config entry needed)
//  3. settings.improve.agent
//  4. an agent whose id is "improver"
//  5. error
//
// It never invents a model. `agents[].model` is required config-wide and there
// is no global default — daemon.New hard-errors with "agent %q: model is
// required" — so a command that guessed one would be picking a model the
// operator never chose, and billing them for it.
func ResolveAdvisor(cfg *config.Config, flags AdvisorFlags) (*Advisor, error) {
	if cfg == nil {
		return nil, fmt.Errorf("no configuration loaded: `apiary improve` needs %s to resolve the analysing agent", "apiary.yaml")
	}

	// 2. Ad-hoc runner/model pair. Checked before the config sources so an
	// explicit command line always wins.
	if flags.Runner != "" || flags.Model != "" {
		if flags.Runner == "" {
			return nil, fmt.Errorf("--model requires --runner: the runner determines which adapter runs the model")
		}
		if flags.Model == "" {
			return nil, fmt.Errorf("--runner requires --model: there is no default model to fall back to")
		}
		rc := findRunner(cfg, flags.Runner)
		if rc == nil {
			return nil, fmt.Errorf("runner %q is not defined in runners (available: %s)",
				flags.Runner, strings.Join(runnerIDs(cfg), ", "))
		}
		if err := checkModelAllowed(rc, flags.Model); err != nil {
			return nil, err
		}
		return &Advisor{
			AgentID:  "(ad-hoc)",
			RunnerID: rc.ID,
			Model:    flags.Model,
			Runner:   rc,
			Source:   "--runner/--model",
		}, nil
	}

	agentID, source := "", ""
	switch {
	case flags.Advisor != "":
		agentID, source = flags.Advisor, "--advisor"
	case cfg.Settings.Improve.Agent != "":
		agentID, source = cfg.Settings.Improve.Agent, "settings.improve.agent"
	case findAgent(cfg, DefaultAdvisorAgentID) != nil:
		agentID, source = DefaultAdvisorAgentID, "agents[improver]"
	default:
		return nil, noAdvisorError(cfg)
	}

	ac := findAgent(cfg, agentID)
	if ac == nil {
		return nil, fmt.Errorf("advisor agent %q (%s) is not defined in agents (available: %s)",
			agentID, source, strings.Join(agentIDs(cfg), ", "))
	}

	runnerID := ac.Runner
	if runnerID == "" {
		runnerID = cfg.DefaultRunner
	}
	if runnerID == "" {
		return nil, fmt.Errorf("agent %q specifies no runner and default_runner is not set", agentID)
	}
	rc := findRunner(cfg, runnerID)
	if rc == nil {
		return nil, fmt.Errorf("agent %q: runner %q is not defined in runners (available: %s)",
			agentID, runnerID, strings.Join(runnerIDs(cfg), ", "))
	}

	model := ac.Model
	if model == "" {
		return nil, fmt.Errorf("agent %q has no model: `model` is required and there is no global default", agentID)
	}
	// Effort may override the model — analysing aggregates and reasoning over
	// prose are different jobs and need not share a model.
	if m := cfg.Settings.Improve.EffortModels[string(flags.Effort)]; m != "" {
		if err := checkModelAllowed(rc, m); err != nil {
			return nil, fmt.Errorf("settings.improve.effort_models.%s: %w", flags.Effort, err)
		}
		model = m
		source += fmt.Sprintf(" + effort_models.%s", flags.Effort)
	}

	fallbacks := ac.Fallbacks
	if len(fallbacks) == 0 {
		fallbacks = cfg.Settings.DefaultFallbacks
	}

	return &Advisor{
		AgentID:   ac.ID,
		RunnerID:  rc.ID,
		Model:     model,
		MaxTurns:  ac.MaxTurns,
		Runner:    rc,
		Agent:     ac,
		Fallbacks: fallbacks,
		Source:    source,
	}, nil
}

// noAdvisorError explains all four ways to name an advisor rather than failing
// with a bare "not found". This is the first wall a new user hits, so it carries
// a config snippet they can paste.
func noAdvisorError(cfg *config.Config) error {
	var b strings.Builder
	b.WriteString("no advisor agent configured — `apiary improve` needs to know which agent performs the analysis.\n\n")
	b.WriteString("Any one of these works:\n")
	b.WriteString("  1. apiary improve --advisor <agent-id>\n")
	b.WriteString("  2. apiary improve --runner <id> --model <name>\n")
	b.WriteString("  3. settings.improve.agent: <agent-id>\n")
	b.WriteString("  4. define an agent with id \"improver\":\n\n")
	b.WriteString("       agents:\n")
	b.WriteString("         - id: improver\n")
	b.WriteString("           description: \"Analyses execution metrics and proposes improvements\"\n")
	b.WriteString("           soul_file: .apiary/agents/improver.md\n")
	b.WriteString("           model: <model>\n")
	if ids := agentIDs(cfg); len(ids) > 0 {
		fmt.Fprintf(&b, "\nAgents currently defined: %s", strings.Join(ids, ", "))
	}
	b.WriteString("\n\n(`--dump-evidence` needs no advisor at all — it runs no model.)")
	return fmt.Errorf("%s", b.String())
}

// checkModelAllowed rejects a model the runner does not list. A runner with no
// `models` block accepts anything, which is the common case.
func checkModelAllowed(rc *config.RunnerConfig, model string) error {
	if len(rc.Models) == 0 {
		return nil
	}
	for _, m := range rc.Models {
		if m == model {
			return nil
		}
	}
	return fmt.Errorf("model %q is not listed for runner %q (allowed: %s)",
		model, rc.ID, strings.Join(rc.Models, ", "))
}

func findAgent(cfg *config.Config, id string) *config.AgentConfig {
	for i := range cfg.Agents {
		if cfg.Agents[i].ID == id {
			return &cfg.Agents[i]
		}
	}
	return nil
}

func findRunner(cfg *config.Config, id string) *config.RunnerConfig {
	for i := range cfg.Runners {
		if cfg.Runners[i].ID == id {
			return &cfg.Runners[i]
		}
	}
	return nil
}

func agentIDs(cfg *config.Config) []string {
	out := make([]string, 0, len(cfg.Agents))
	for _, a := range cfg.Agents {
		out = append(out, a.ID)
	}
	sort.Strings(out)
	return out
}

func runnerIDs(cfg *config.Config) []string {
	out := make([]string, 0, len(cfg.Runners))
	for _, r := range cfg.Runners {
		out = append(out, r.ID)
	}
	sort.Strings(out)
	return out
}
