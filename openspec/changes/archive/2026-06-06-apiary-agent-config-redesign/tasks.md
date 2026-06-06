# Tasks: Agent Configuration Redesign

## 1. Config Structure

- [x] 1.1 Add `AgentConfig` struct to `src/internal/config/config.go` with fields: `ID`, `Description`, `SoulFile`, `PreferredModels`, `Skills`
- [x] 1.2 Update `Config` struct to include `Agents []AgentConfig` field
- [x] 1.3 Update `RouteConfig` struct: add `Agent` field (string) for agent references
- [x] 1.4 Update YAML unmarshaling to parse agent field in routes

## 2. Config Validation

- [x] 2.1 Add agent ID uniqueness validation in `src/internal/config/validate.go`
- [x] 2.2 Add soul file path validation: check file exists and is readable
- [x] 2.3 Add preferred models validation: non-empty list required
- [x] 2.4 Update route validation: check `Route.Agent` references a defined agent ID
- [x] 2.5 Add validation error case: route must have `Agent` field
- [x] 2.6 Write validation tests covering all new cases

## 3. Dispatcher Initialization

- [x] 3.1 Update `Dispatcher.New()` in `src/internal/daemon/dispatcher.go` to instantiate runners from agents
- [x] 3.2 For each agent, create a pseudo-worker with ID `agent-{agentID}` using the agent's first preferred model
- [x] 3.3 Instantiate runner for each pseudo-worker using agent's first preferred model
- [x] 3.4 Update dispatcher's runner map to store runners under pseudo-worker IDs
- [x] 3.5 Update logging to show agent info at startup (IDs, preferred models)

## 4. Dispatcher Dispatch Logic

- [x] 4.1 Update `Dispatcher.dispatch()` to handle agent-based routes
- [x] 4.2 When dispatching, look up agent by ID from route
- [x] 4.3 Read agent's soul file and append to `RunRequest.SystemAppend`
- [x] 4.4 Handle soul file load errors gracefully: log error but continue dispatch with base system prompt
- [x] 4.5 Add debug logging showing soul file content (in verbose mode)

## 5. Router Updates

- [x] 5.1 Update `Router.Route()` in `src/internal/router/router.go` to work with agent references
- [x] 5.2 Verify route matching logic is unchanged (routes still match by source/labels/types/priority/regex)
- [x] 5.3 Update router tests to verify routes reference agents correctly

## 6. Model Struct Updates

- [x] 6.1 Add optional `AgentMetadata` field to `RunRequest` in `src/internal/model/result.go` (for future use)
- [x] 6.2 Document that runner receives agent's first preferred model

## 7. Testing

- [x] 7.1 Write unit tests for `AgentConfig` validation in `src/internal/config/validate_test.go`
- [x] 7.2 Write integration test: load config with agents, verify routes dispatch to correct agents
- [x] 7.3 Write test for soul file loading and injection into system prompt
- [x] 7.4 Write test for soul file not found error handling
- [x] 7.5 Test agent model selection: verify first preferred model is used

## 8. CLI Updates

- [x] 8.1 Update `apiary validate` command to test agent definitions and soul files
- [x] 8.2 Update `apiary status` or similar to show loaded agents and their models
- [x] 8.3 Verify `apiary run --dry-run` works with new agent-based routes

## 9. Documentation & Examples

- [x] 9.1 Create example `apiary.yaml` showing agents section with multiple agents using `.apiary/souls/` paths
- [x] 9.2 Create example soul files in `.apiary/souls/` directory (e.g., `.apiary/souls/frontend.md`, `.apiary/souls/backend.md`)
- [x] 9.3 Document soul file best practices: keep concise, focus on expertise/personality
- [x] 9.4 Document `.apiary/` directory structure: explain that it's the default location for all extra configs

## 10. Integration & Smoke Tests

- [x] 10.1 Run full test suite: `go test ./...`
- [x] 10.2 Test with real Plane instance: agents successfully dispatch tasks and post results
- [x] 10.3 End-to-end test: agent with soul file executes task and system prompt is visible in agent output

## 11. Cleanup & Polish

- [x] 11.1 Remove any debug logging added during implementation
- [x] 11.2 Review error messages for clarity
- [x] 11.3 Check code style and consistency
