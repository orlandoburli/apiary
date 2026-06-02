# Apiary Runner Configuration Design

## Problem

Currently, Apiary agents are hardcoded to use the Claude CLI runner with fixed configuration:
- Command: `claude`
- Model flag: `--model`
- Prompt flag: `-p`

This means:
- **No flexibility** to use Claude API, OpenCode API, or other providers
- **Can't configure** CLI flags per runner type
- **Hardcoded assumptions** about how Claude CLI works
- **No way to** specify API keys, endpoints, or provider-specific settings

## Solution

Add a top-level `runners` configuration section to `apiary.yaml` that defines:
1. **Runner types** (cli, api, opencode, etc.)
2. **Runner-specific config** (command, flags, endpoints, auth)
3. **Default runner** for agents
4. **Per-agent overrides** if needed

## Proposed Configuration

### Config Structure

Add to `config.go`:

```go
type Config struct {
    Version  string
    Runners  []RunnerConfig  // NEW: runner definitions
    Sources  []SourceConfig
    Agents   []AgentConfig
    Workers  []WorkerConfig
    Routes   []RouteConfig
    Settings Settings
}

type RunnerConfig struct {
    ID       string            // "cli", "claude-api", "opencode", etc.
    Type     string            // Runner type: "cli", "api", "llm-gateway", etc.
    Config   map[string]any    // Runner-specific settings
}

// Update AgentConfig to allow runner override:
type AgentConfig struct {
    ID              string
    Description     string
    SoulFile        string
    PreferredModels []string
    Skills          []string
    Runner          string            // NEW: optional runner ID (if not specified, use default)
}
```

### Example YAML

```yaml
version: "1"

# ── Runners ────────────────────────────────────────────────────────────────────
runners:
  # Claude CLI runner (local development, no API keys)
  - id: claude-cli
    type: cli
    config:
      command: claude
      model_flag: --model
      prompt_flag: -p
      # Optional Claude CLI overrides
      # working_dir: /path/to/project
      # max_tokens: 8000

  # Claude API runner (Claude 4 models via Anthropic API)
  - id: claude-api
    type: api
    config:
      provider: anthropic
      base_url: https://api.anthropic.com
      api_key: ${ANTHROPIC_API_KEY}
      # Model mapping: provider model names
      models:
        claude-opus-4-8: claude-4-20250514
        claude-sonnet-4-6: claude-3-5-sonnet-20241022
        claude-haiku-4-5: claude-3-5-haiku-20241022

  # OpenCode runner (deepseek, llama, other models)
  - id: opencode-runner
    type: api
    config:
      provider: opencode-go
      base_url: https://opencode.ai/zen/go/v1
      api_key: ${OPENCODE_API_KEY}
      # Model mapping
      models:
        deepseek-v4-flash: deepseek-chat

  # Hermes kanban runner (via hermes-cli for kanban integration)
  - id: hermes-kanban
    type: hermes-cli
    config:
      profile: engineer
      # Hermes profile configuration
      # auto_commit: true
      # workspace: /path/to/kanban

# ── Default Runner ────────────────────────────────────────────────────────────
default_runner: claude-cli  # Used if agent.runner not specified

# ── Agents ────────────────────────────────────────────────────────────────────
agents:
  - id: investigator
    description: "Investigator — analyzes and classifies tasks by complexity"
    soul_file: .apiary/souls/investigator.md
    preferred_models: [claude-opus-4-8, claude-sonnet-4-6]
    skills: [gitnexus-codebase]
    # Uses default_runner (claude-cli)

  - id: engineer
    description: "Engineer — implements tasks"
    soul_file: .apiary/souls/engineer.md
    preferred_models: [claude-sonnet-4-6, claude-haiku-4-5]
    skills: [git-workflow, e2e-before-pr]
    runner: claude-cli  # Explicit (optional)

  - id: hermes-engineer
    description: "Hermes Engineer — from Hermes kanban"
    soul_file: .apiary/souls/hermes-engineer.md
    preferred_models: [claude-opus-4-8]
    skills: [git-workflow]
    runner: hermes-kanban  # Override: use Hermes CLI runner

  - id: api-agent
    description: "Agent using Claude API (not CLI)"
    soul_file: .apiary/souls/api-agent.md
    preferred_models: [claude-opus-4-8]
    runner: claude-api  # Use Claude API instead of CLI
```

## Implementation Plan

### Step 1: Update Config Struct
- Add `RunnerConfig` struct to `config.go`
- Add `Runners []RunnerConfig` field to `Config`
- Add optional `Runner string` field to `AgentConfig`
- Add `DefaultRunner string` to `Settings` (or top-level)

### Step 2: Update Validation
- Validate runner IDs are unique
- Validate runner types are known ("cli", "api", "hermes-cli", etc.)
- Validate each agent.runner references a defined runner ID
- Validate default_runner exists

### Step 3: Update Dispatcher
- Load runner definitions from config
- For each agent:
  - Look up runner by ID (use default if not specified)
  - Instantiate runner with agent's runner config
  - Store runner for dispatch

### Step 4: Update Runner Interface
- Extend `runner.Adapter` to accept configuration map
- Runners parse their specific config (endpoints, API keys, etc.)
- CLI runner: parse command, model_flag, prompt_flag
- API runner: parse provider, base_url, api_key, model mappings

## Runner Types

### 1. CLI Runner (`cli`)
Executes local CLI command (e.g., `claude` CLI)

```yaml
runners:
  - id: claude-cli
    type: cli
    config:
      command: claude              # CLI command to run
      model_flag: --model          # Flag for model selection
      prompt_flag: -p              # Flag for prompt/stdin
```

### 2. API Runner (`api`)
HTTP API-based execution (Claude API, OpenCode, etc.)

```yaml
runners:
  - id: claude-api
    type: api
    config:
      provider: anthropic
      base_url: https://api.anthropic.com/v1
      api_key: ${ANTHROPIC_API_KEY}
      models:
        claude-opus-4-8: claude-4-20250514
```

### 3. Hermes CLI Runner (`hermes-cli`)
Integration with Hermes kanban system

```yaml
runners:
  - id: hermes-kanban
    type: hermes-cli
    config:
      profile: engineer              # Hermes profile to use
      workspace: ~/.hermes/profiles  # Hermes workspace
```

### 4. HTTP Gateway Runner (`gateway`)
Custom HTTP gateway for LLM routing

```yaml
runners:
  - id: gateway
    type: gateway
    config:
      endpoint: https://internal-gateway.example.com/run
      auth_header: Authorization: Bearer ${GATEWAY_TOKEN}
```

## Migration Path

### Phase 1: Optional (Current)
- Add `runners` config section
- Default_runner = "claude-cli" if not specified
- All agents use default_runner if agent.runner not specified
- Backward compatible: existing configs still work

### Phase 2: Recommended (Next)
- Update example configs to explicitly specify runners
- Document runner configuration
- Provide examples for each runner type

### Phase 3: Future
- Support runner fallback chain (primary → fallback1 → fallback2)
- Model availability detection (check if model available before dispatch)
- Dynamic runner selection based on model availability

## Benefits

| Aspect | Before | After |
|--------|--------|-------|
| **Runner flexibility** | Hardcoded Claude CLI | Any runner type (CLI, API, Hermes, custom) |
| **Provider support** | Only Claude CLI | Anthropic API, OpenCode, Hermes, custom gates |
| **Configuration** | Hidden in code | Explicit in YAML |
| **Model mapping** | Hardcoded | Configurable per runner |
| **Agent customization** | All use same runner | Each agent can choose runner |
| **Testing** | Limited to CLI | Test with different providers easily |

## Example Use Cases

### Use Case 1: Local Development with Claude CLI
```yaml
default_runner: claude-cli
# No API keys needed, uses local CLI
```

### Use Case 2: Production with Claude API
```yaml
default_runner: claude-api
runners:
  - id: claude-api
    type: api
    config:
      provider: anthropic
      api_key: ${ANTHROPIC_API_KEY}
```

### Use Case 3: Mixed Providers
```yaml
agents:
  - id: expensive-task
    runner: claude-api  # Use paid Anthropic API
    preferred_models: [claude-opus-4-8]

  - id: simple-task
    runner: opencode   # Use cheaper OpenCode
    preferred_models: [deepseek-v4-flash]
```

### Use Case 4: Hermes Integration
```yaml
runners:
  - id: hermes-kanban
    type: hermes-cli
    config:
      profile: engineer

agents:
  - id: hermes-engineer
    runner: hermes-kanban  # Use Hermes kanban integration
```

## Questions for Design Approval

1. **Config location**: Should runner config be in main `apiary.yaml` or separate `runners.yaml`?
2. **Model mapping**: Should each runner define model mappings, or should agents specify provider-specific model names?
3. **Fallback chain**: Should we support trying runners in sequence if primary fails?
4. **Secrets**: How to handle API keys — env vars, .env file, secure vault?
5. **Discovery**: Should runners auto-detect available models at startup?

## Implementation Effort

- **Config changes**: ~20 lines (add structs)
- **Validation**: ~30 lines (unique IDs, referential integrity)
- **Dispatcher**: ~50 lines (runner lookup, instantiation)
- **Documentation**: ~3 example files
- **Testing**: Test file per runner type

**Estimated**: 4-6 hours to fully implement + test
