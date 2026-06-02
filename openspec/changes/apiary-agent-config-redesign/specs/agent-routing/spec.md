# Specification: Agent Routing

Route tasks to agents by ID; enable flexible assignment and multi-route agent reuse.

## ADDED Requirements

### Requirement: Routes reference agents
The system SHALL support `agent: <agent-id>` field in route definitions. Routes use this field to specify which agent handles matched tasks.

#### Scenario: Route with agent reference
- **WHEN** route contains:
  ```yaml
  routes:
    - id: frontend-task
      priority: 10
      match:
        labels: [type:frontend]
      agent: frontend-engineer
  ```
- **THEN** the route is parsed successfully and references the agent

### Requirement: Agent reference validation
The system SHALL validate that each route's `agent:` field references a defined agent ID. References to non-existent agents SHALL cause config validation to fail.

#### Scenario: Valid agent reference
- **WHEN** route specifies `agent: frontend-engineer` and an agent with that ID is defined
- **THEN** validation passes

#### Scenario: Invalid agent reference
- **WHEN** route specifies `agent: nonexistent-agent` and no such agent is defined
- **THEN** validation fails with error indicating agent not found

### Requirement: Multiple routes can use the same agent
The system SHALL allow multiple routes to reference the same agent. One agent can handle tasks from different sources or with different label/state combinations.

#### Scenario: Two routes use same agent
- **WHEN** two routes both specify `agent: frontend-engineer` with different `match:` criteria
- **THEN** both routes are valid and the agent handles both task types

#### Scenario: Task dispatch to correct agent
- **WHEN** a task matches route A and route A references `agent: frontend-engineer`
- **THEN** task is dispatched to frontend-engineer agent (not any other agent)

### Requirement: Route priority ordering remains unchanged
The system SHALL evaluate routes in priority order (lower priority number evaluated first) regardless of agent references. Routing behavior is unchanged; only the destination (agent ID instead of worker ID) changes.

#### Scenario: Priority ordering with agents
- **WHEN** config has:
  - route A: priority 10, `agent: agent1`
  - route B: priority 5, `agent: agent2`
- **THEN** route B is evaluated first; if task matches, agent2 handles it

### Requirement: Routes must specify agent
Every route MUST specify an `agent:` field. Routes without an agent SHALL fail validation.

#### Scenario: Route with agent field
- **WHEN** route specifies `agent: frontend-engineer`
- **THEN** route is valid

#### Scenario: Route missing agent field
- **WHEN** route does not specify `agent:` field
- **THEN** validation fails with error indicating agent field is required

### Requirement: Agent model selection at dispatch
When a route references an agent, the system SHALL use the agent's first preferred model for task execution.

#### Scenario: First preferred model is used
- **WHEN** agent specifies `preferred_models: [sonnet, haiku]` and a task matches a route to this agent
- **THEN** task is executed using the sonnet model
