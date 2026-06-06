# Specification: Agent Soul Files

Load and inject agent personality/expertise guidelines from external markdown files into system prompts.

## ADDED Requirements

### Requirement: Soul file loading
The system SHALL load the agent's `soul_file` (if specified) at dispatch time and inject its contents into the agent's system prompt.

#### Scenario: Soul file loaded and injected
- **WHEN** agent specifies `soul_file: .apiary/souls/frontend.md` and a task is dispatched to this agent
- **THEN** the contents of `.apiary/souls/frontend.md` are read and appended to the runner's system prompt

#### Scenario: Task without soul file
- **WHEN** agent does not specify a `soul_file` and a task is dispatched to this agent
- **THEN** system prompt contains no injected soul file content

### Requirement: Soul file content format
Soul files SHALL be markdown (`.md`) documents. The system SHALL treat the entire file content as text to be injected into the system prompt, without requiring any specific markdown structure.

#### Scenario: Soul file with arbitrary markdown
- **WHEN** soul file contains:
  ```markdown
  # Frontend Agent Personality
  
  You are an expert in React and component design.
  Always use web-componentes-ui suite.
  Prefer composition over inheritance.
  ```
- **THEN** all content is injected as-is into the system prompt

### Requirement: Soul file content appended to system prompt
Soul file contents SHALL be appended to any base system prompt and the route's `system_append:` configuration.

#### Scenario: Soul file appends to system append
- **WHEN** route specifies `system_append: "Follow project conventions"` and agent specifies `soul_file: agents/souls/frontend.md`
- **THEN** system prompt contains: `Follow project conventions` + soul file contents

### Requirement: Late binding of soul files
Soul files are read at dispatch time, not at daemon startup. Changes to soul files become effective on the next task dispatch without restarting the daemon.

#### Scenario: Soul file change without restart
- **WHEN** daemon is running and user updates `agents/souls/frontend.md`
- **THEN** next task dispatched to this agent receives the updated soul file content without daemon restart

### Requirement: Soul file load errors
If a soul file fails to load at dispatch time (file deleted, permissions issues, etc.), the system SHALL log an error but continue dispatch with an empty soul file. The agent executes with base system prompt only.

#### Scenario: Soul file not found at dispatch
- **WHEN** agent specifies `soul_file: agents/souls/deleted.md` but file is deleted between startup and dispatch
- **THEN** system logs error, agent executes with base system prompt (no soul file content)

### Requirement: Visibility of soul file content
When appropriate (e.g., in dry-run mode or verbose logging), the system SHALL make soul file content visible to help users debug system prompt construction.

#### Scenario: Dry-run shows soul file content
- **WHEN** user runs `apiary run --dry-run --verbose`
- **THEN** output includes information about which soul files would be injected (optionally showing truncated content)

### Requirement: Soul file paths are relative to config directory
Soul file paths are relative to the directory containing `apiary.yaml`. By convention, soul files are stored in `.apiary/souls/` subdirectory.

#### Scenario: Relative path resolution
- **WHEN** `apiary.yaml` is at `/home/user/apiary/apiary.yaml` and agent specifies `soul_file: .apiary/souls/frontend.md`
- **THEN** system loads `/home/user/apiary/.apiary/souls/frontend.md`
