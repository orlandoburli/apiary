package improve

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
	runnerimpl "github.com/orlandoburli/apiary/internal/runner"
	"github.com/orlandoburli/apiary/internal/runner/execution"
)

// RunOutcome is the result of one advisor invocation.
type RunOutcome struct {
	Analysis Analysis
	// Attempts records every runner invocation, including the ones rejected by a
	// provider before a fallback took over.
	Attempts []AttemptRecord
	Usage    model.Usage
	Duration time.Duration
	// RawOutput is the advisor's text with the sentinel stripped. Kept so a run
	// that produced no parseable output can still be inspected.
	RawOutput string
	// Structured is the raw APIARY_OUTPUT object, before it is decoded into a
	// particular shape. The analysis pass decodes it into an Analysis; the critic
	// pass decodes the same field into a CritiqueSet.
	Structured map[string]any
}

// AttemptRecord is one runner invocation.
type AttemptRecord struct {
	RunnerID string
	Adapter  string
	Model    string
	Success  bool
	Failure  string
	Err      string
	Duration time.Duration
}

// RunAdvisor invokes the advisor agent and parses its structured output.
//
// It mirrors the dispatcher's construction path — resolve the runner block,
// derive the adapter name, instantiate, Configure with the MCP/sandbox/env
// overlay — but deliberately does not go through the dispatcher, queue or
// workflow engine: this is a one-shot analysis that must work with the daemon
// stopped and must not consume a dispatch slot when it is running.
func RunAdvisor(ctx context.Context, cfg *config.Config, adv *Advisor, prompt string, k Knobs, workDir string) (*RunOutcome, error) {
	candidates := candidateChain(cfg, adv)
	out := &RunOutcome{}
	started := time.Now()

	var lastErr error
	for _, c := range candidates {
		res, err := runOnce(ctx, cfg, c, prompt, k, workDir)

		rec := AttemptRecord{RunnerID: c.runnerID, Adapter: c.adapter, Model: c.model}
		if res != nil {
			rec.Duration = res.Duration
			rec.Success = res.Success
			if res.Usage != nil {
				addUsage(&out.Usage, res.Usage)
			}
		}
		if err != nil {
			rec.Err = err.Error()
		}

		// Classify the failure the same way the daemon does, so a provider
		// rejection advances the chain instead of aborting the whole run. A deep
		// analysis that trips the 5-hour limit halfway through must not discard
		// what it already spent.
		if res != nil {
			kind, _ := execution.FailureDetectorFor(c.adapter).Detect(model.RunRequest{}, res)
			if kind != model.FailureNone {
				rec.Failure = kind.String()
			}
			out.Attempts = append(out.Attempts, rec)

			if kind == model.FailureRateLimited || kind == model.FailureCreditExhausted {
				lastErr = fmt.Errorf("runner %s: %s", c.runnerID, kind)
				continue
			}
			if res.Success {
				out.Duration = time.Since(started)
				out.RawOutput = res.Output
				out.Structured = res.StructuredOutput
				if res.StructuredOutput == nil {
					return out, fmt.Errorf("advisor produced no APIARY_OUTPUT block; "+
						"see the raw output above (%d bytes)", len(res.Output))
				}
				analysis, err := decodeAnalysis(res.StructuredOutput)
				if err != nil {
					return out, err
				}
				out.Analysis = analysis
				return out, nil
			}
			lastErr = fmt.Errorf("runner %s failed: %v", c.runnerID, res.Error)
			continue
		}

		out.Attempts = append(out.Attempts, rec)
		lastErr = err
	}

	out.Duration = time.Since(started)
	if lastErr == nil {
		lastErr = fmt.Errorf("no runner candidates available")
	}
	return out, lastErr
}

// runStructured performs one advisor invocation for a prompt whose output shape
// is decided by the caller. RunAdvisor is the analysis-shaped wrapper around it;
// the critic pass uses this directly.
func runStructured(ctx context.Context, cfg *config.Config, adv *Advisor, prompt string, k Knobs, workDir string) (*RunOutcome, error) {
	out := &RunOutcome{}
	started := time.Now()
	var lastErr error

	for _, c := range candidateChain(cfg, adv) {
		res, err := runOnce(ctx, cfg, c, prompt, k, workDir)
		rec := AttemptRecord{RunnerID: c.runnerID, Adapter: c.adapter, Model: c.model}
		if err != nil {
			rec.Err = err.Error()
		}
		if res == nil {
			out.Attempts = append(out.Attempts, rec)
			lastErr = err
			continue
		}

		rec.Duration = res.Duration
		rec.Success = res.Success
		if res.Usage != nil {
			addUsage(&out.Usage, res.Usage)
		}
		kind, _ := execution.FailureDetectorFor(c.adapter).Detect(model.RunRequest{}, res)
		if kind != model.FailureNone {
			rec.Failure = kind.String()
		}
		out.Attempts = append(out.Attempts, rec)

		if kind == model.FailureRateLimited || kind == model.FailureCreditExhausted {
			lastErr = fmt.Errorf("runner %s: %s", c.runnerID, kind)
			continue
		}
		if res.Success {
			out.Duration = time.Since(started)
			out.RawOutput = res.Output
			out.Structured = res.StructuredOutput
			if res.StructuredOutput == nil {
				return out, fmt.Errorf("agent produced no APIARY_OUTPUT block (%d bytes of output)", len(res.Output))
			}
			return out, nil
		}
		lastErr = fmt.Errorf("runner %s failed: %v", c.runnerID, res.Error)
	}

	out.Duration = time.Since(started)
	if lastErr == nil {
		lastErr = fmt.Errorf("no runner candidates available")
	}
	return out, lastErr
}

type candidate struct {
	runnerID string
	adapter  string
	model    string
	rc       *config.RunnerConfig
	agent    *config.AgentConfig
}

// candidateChain is the primary runner followed by its fallbacks, skipping any
// fallback whose runner is not defined.
func candidateChain(cfg *config.Config, adv *Advisor) []candidate {
	out := []candidate{{
		runnerID: adv.RunnerID, adapter: adv.Runner.AdapterName(),
		model: adv.Model, rc: adv.Runner, agent: adv.Agent,
	}}
	for _, fb := range adv.Fallbacks {
		rc := findRunner(cfg, fb.Runner)
		if rc == nil {
			continue
		}
		m := fb.Model
		if m == "" {
			m = adv.Model
		}
		out = append(out, candidate{
			runnerID: rc.ID, adapter: rc.AdapterName(), model: m, rc: rc, agent: adv.Agent,
		})
	}
	return out
}

func runOnce(ctx context.Context, cfg *config.Config, c candidate, prompt string, k Knobs, workDir string) (*model.RunResult, error) {
	ra, ok := runnerimpl.New(c.adapter)
	if !ok {
		return nil, fmt.Errorf("runner adapter %q not registered (runner %q)", c.adapter, c.runnerID)
	}

	var agentMCPs []model.MCPServer
	var env map[string]string
	maxTurns := k.MaxTurns
	if c.agent != nil {
		agentMCPs = c.agent.MCPs
		env = c.agent.Env
		if maxTurns == 0 {
			maxTurns = c.agent.MaxTurns
		}
	}

	cfgMap := runnerConfigWithMCPs(c.rc.Config, c.rc.MCPs, agentMCPs)
	cfgMap = injectRunnerSecurity(cfgMap, c.rc.Sandbox, c.rc.EnvPassthrough)
	if err := ra.Configure(cfgMap); err != nil {
		return nil, fmt.Errorf("configure runner %q: %w", c.runnerID, err)
	}

	soul := ""
	if c.agent != nil && c.agent.SoulFile != "" {
		if data, err := os.ReadFile(c.agent.SoulFile); err == nil {
			soul = string(data)
		}
	}
	if soul == "" {
		soul = DefaultAdvisorSoul
	}

	req := model.RunRequest{
		Cell: model.SourceItem{
			ID:    "improve",
			Title: "Pipeline self-improvement analysis",
		},
		WorkerID:      "improve",
		Model:         c.model,
		MaxTurns:      maxTurns,
		SystemPrepend: soul,
		SystemAppend:  prompt,
		WorkingDir:    workDir,
		Env:           env,
	}

	res, err := ra.Run(ctx, req)
	return &res, err
}

// decodeInto converts a parsed structured output into a typed shape.
// Round-tripping through JSON keeps the field mapping in one place (the struct
// tags) rather than duplicating it as map lookups.
func decodeInto(structured map[string]any, dst any) error {
	raw, err := json.Marshal(structured)
	if err != nil {
		return fmt.Errorf("re-encode agent output: %w", err)
	}
	return json.Unmarshal(raw, dst)
}

// decodeAnalysis converts the runner's parsed structured output into the typed
// analysis.
func decodeAnalysis(structured map[string]any) (Analysis, error) {
	var a Analysis
	if err := decodeInto(structured, &a); err != nil {
		return Analysis{}, fmt.Errorf("advisor output did not match the expected shape: %w", err)
	}
	if len(a.Findings) == 0 && len(a.Recommendations) == 0 {
		return a, fmt.Errorf("advisor returned neither findings nor recommendations")
	}
	return a, nil
}

func addUsage(dst *model.Usage, src *model.Usage) {
	dst.InputTokens += src.InputTokens
	dst.OutputTokens += src.OutputTokens
	dst.TotalTokens += src.TotalTokens
	dst.CacheCreationTokens += src.CacheCreationTokens
	dst.CacheReadTokens += src.CacheReadTokens
	dst.NumTurns += src.NumTurns
	dst.NumToolCalls += src.NumToolCalls
	dst.CostUSD += src.CostUSD
}

// runnerConfigWithMCPs merges runner-scope and agent-scope MCP servers into the
// runner's config map, agent overriding runner by name.
//
// This mirrors the daemon helper of the same name. It is duplicated rather than
// shared because the daemon's copy is a private method on a package whose
// constructor is a critical-risk symbol; hoisting it is a separate change.
func runnerConfigWithMCPs(base map[string]any, runnerMCPs, agentMCPs []model.MCPServer) map[string]any {
	merged := model.MergeMCPServers(runnerMCPs, agentMCPs)
	if len(merged) == 0 {
		return base
	}
	out := make(map[string]any, len(base)+1)
	for k, v := range base {
		out[k] = v
	}
	out["mcps"] = merged
	return out
}

// injectRunnerSecurity threads the sandbox and env-passthrough settings into the
// runner config, so an advisor run is contained exactly like a daemon run.
func injectRunnerSecurity(base map[string]any, sandbox *config.SandboxConfig, envPassthrough []string) map[string]any {
	if sandbox == nil && len(envPassthrough) == 0 {
		return base
	}
	out := make(map[string]any, len(base)+2)
	for k, v := range base {
		out[k] = v
	}
	if sandbox != nil {
		out["sandbox"] = sandbox
	}
	if len(envPassthrough) > 0 {
		out["env_passthrough"] = envPassthrough
	}
	return out
}

// DescribeAttempts renders the runner chain for the cost line, naming any
// fallback that had to take over.
func (o *RunOutcome) DescribeAttempts() string {
	if len(o.Attempts) <= 1 {
		return ""
	}
	parts := make([]string, 0, len(o.Attempts))
	for _, a := range o.Attempts {
		s := a.RunnerID
		if a.Failure != "" {
			s += " (" + a.Failure + ")"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " → ")
}

// AddUsage folds a second pass's usage into this outcome, so the reported cost
// covers the whole analysis rather than only its first call.
func (o *RunOutcome) AddUsage(u model.Usage) {
	addUsage(&o.Usage, &u)
}
