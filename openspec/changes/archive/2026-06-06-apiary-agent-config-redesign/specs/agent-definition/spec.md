# Specification: Agent Definition

Define agents with preferred models, skills, and soul files; decouple agent lifecycle from routing.

## ADDED Requirements

### Requirement: Agent configuration schema
The system SHALL support an `agents:` top-level configuration section in `apiary.yaml` containing an array of agent definitions.

#### Scenario: Valid agent definition
- **WHEN** `apiary.yaml` contains:
  ```yaml
  agents:
    - id: frontend-engineer
      description: React specialist
      soul_file: .apiary/souls/frontend.md
      preferred_models: [claude-sonnet-4-6, claude-haiku-4-5]
      skills: [web-componentes-ui, campos-monetarios-br]
  ```
- **THEN** the agent is parsed successfully and stored in the config

### Requirement: Agent ID uniqueness validation
The system SHALL validate that all agent IDs within the `agents:` array are unique. Duplicate agent IDs SHALL cause config validation to fail.

#### Scenario: Duplicate agent IDs
- **WHEN** config contains two agents with the same `id: agent1`
- **THEN** validation fails with error message indicating duplicate agent ID

### Requirement: Agent soul file validation
The system SHALL validate that each agent's `soul_file` path (if specified) points to an existing, readable file. Non-existent or unreadable soul files SHALL cause config validation to fail.

#### Scenario: Valid soul file path
- **WHEN** agent specifies `soul_file: .apiary/souls/frontend.md` and that file exists
- **THEN** validation passes

#### Scenario: Invalid soul file path
- **WHEN** agent specifies `soul_file: .apiary/souls/nonexistent.md` and that file does not exist
- **THEN** validation fails with error indicating file not found

### Requirement: Preferred models list validation
The system SHALL require each agent to have a non-empty `preferred_models` list. Empty or missing `preferred_models` SHALL cause config validation to fail.

#### Scenario: Valid preferred models
- **WHEN** agent specifies `preferred_models: [claude-sonnet-4-6, claude-haiku-4-5]`
- **THEN** validation passes

#### Scenario: Empty preferred models
- **WHEN** agent specifies `preferred_models: []` or omits the field
- **THEN** validation fails with error indicating at least one preferred model is required

### Requirement: Agent description and skills are optional metadata
The system SHALL allow agents to omit `description` and `skills` fields. These fields are optional metadata for documentation and future routing enhancements.

#### Scenario: Minimal agent definition
- **WHEN** agent specifies only `id`, `preferred_models`, and no other fields
- **THEN** the agent is parsed successfully

#### Scenario: Agent with full metadata
- **WHEN** agent specifies all fields including `description` and `skills`
- **THEN** all fields are preserved in the config

### Requirement: Agent display and logging
The system SHALL include agent information (ID, description, preferred models) in daemon logs and status output for visibility.

#### Scenario: Agent in log output
- **WHEN** daemon starts and loads agents
- **THEN** log entries show agent IDs and their preferred models (e.g., "loaded agent frontend-engineer: [sonnet, haiku]")

#### Scenario: Agent in status command
- **WHEN** user runs `apiary status` or similar
- **THEN** output includes list of agents and their preferred models
