# Bidirectional visual workflow editor

## Motivation

Apiary's VS Code extension renders workflow Mermaid diagrams but cannot author
them. Editing complex branches, retries, waits, approvals, and subworkflows by
hand remains difficult, while introducing a database-owned graph would split the
source of truth from reviewable repository YAML.

## Scope

- Upgrade the existing preview into an optional visual editing mode.
- Parse YAML through a comment-preserving document AST and mutate only supported
  paths so unrelated text and unsupported constructs are never discarded.
- Detect supported versus unsupported workflow constructs and force affected
  workflows into read-only mode with an explicit reason.
- Use the repository `schema/apiary.json` for forms and immediate diagnostics,
  supplemented by workflow reference and expression checks.
- Support triggers, agent steps, branches, retry loops, waits, approvals, and
  subworkflow calls through node forms and connection fields.
- Show generated YAML and a semantic path diff before applying a workspace edit.
- Invoke the installed Apiary CLI for authoritative validation and dry-run.
- Add deterministic round-trip, unsupported-content, validation-location, and
  semantic-diff tests.

The repository YAML remains canonical. Collaborative multi-user editing,
free-form canvas positioning persistence, and a browser-hosted control-plane UI
are deferred.
