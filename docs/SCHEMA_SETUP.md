# Apiary YAML Schema & Validation

This guide covers schema validation and autocomplete setup for `apiary.yaml` files.

## What's Included

1. **JSON Schema** (`schema/apiary.json`) — Complete schema with all config options, types, and constraints
2. **VS Code Integration** (`.vscode/settings.json`) — Automatic schema validation and autocomplete
3. **Pre-commit Hook** (`.git/hooks/pre-commit`) — Validates `apiary.yaml` before commits

## VS Code Autocomplete & Validation

### Prerequisites

Install the YAML extension for VS Code (if not already installed):
- [YAML by Red Hat](https://marketplace.visualstudio.com/items?itemName=redhat.vscode-yaml)

### Setup

The schema is automatically configured in `.vscode/settings.json` to validate `apiary.yaml` files:

```json
{
  "yaml.schemas": {
    "${workspaceFolder}/schema/apiary.json": "apiary.yaml"
  }
}
```

### Usage

Once configured:
- **Real-time validation** — Errors appear as red squiggles in the editor
- **Autocomplete** — Press `Ctrl+Space` (or `Cmd+Space` on Mac) to see available fields
- **Inline help** — Hover over fields to see descriptions and valid values
- **Quick fixes** — VS Code suggests fixes for common errors

### Example Autocomplete

Start typing in `apiary.yaml`:
```yaml
agents:
  - id: my-agent
    model: claude-opus-4-8
    # Press Ctrl+Space here to see valid fields:
    # - description
    # - soul_file
    # - skills
    # - runner
    # - max_workers
    # - source_token
    # - source_email
    # - source_name
```

## CLI Validation

Validate your config from the command line:

```bash
apiary validate
# or with explicit path:
apiary validate --config .apiary/apiary.yaml
```

Output example:
```
✓ config is valid
```

On error:
```
  ✗ agents[0] "classifier": model is required
  ✗ runners[0]: duplicate id "claude"
  ✗ default_runner "missing": not defined in runners
3 validation error(s)
```

## Pre-commit Hook

Automatically validates `apiary.yaml` files before they're committed.

### Setup

The hook is already installed at `.git/hooks/pre-commit`. To enable it, make sure it's executable:

```bash
chmod +x .git/hooks/pre-commit
```

### How It Works

When you run `git commit`:
1. Hook checks if any `apiary.yaml` files are staged
2. Extracts the staged version and validates it
3. Blocks commit if validation fails
4. Allows commit if validation passes

### Example

```bash
$ git add apiary.yaml
$ git commit -m "Fix agents config"

🔍 Validating apiary.yaml...
  Validating: apiary.yaml
❌ Validation failed for apiary.yaml
   agents[1] "worker": runner "missing" not defined

# Fix the error, then try again
$ git commit -m "Fix agents config"

🔍 Validating apiary.yaml...
  Validating: apiary.yaml
✅ apiary.yaml validation passed
[main abc1234] Fix agents config
```

### Bypass (Not Recommended)

To skip the hook (e.g., for WIP commits):

```bash
git commit --no-verify
```

## Schema Structure

### Top-level Fields

| Field | Required | Description |
|-------|----------|-------------|
| `version` | Yes | Schema version (e.g., "1.0") |
| `runners` | No | Runner definitions |
| `sources` | No | Task sources (GitHub, Jira, Linear, etc.) |
| `agents` | No | Agent definitions |
| `default_runner` | No | Default runner ID used when an agent omits `runner` |
| `workflows` | No | Workflow definitions |
| `settings` | No | Global settings |
| `tasks` | No | Task-level hooks |

### Key Validations

The schema enforces:

- **Required fields** — `id`, `type`, `model`, `runner` where applicable
- **Enum values** — Step types, resume policies, publish modes, etc.
- **Cross-references** — `default_runner` must exist in `runners`, agent `runner` must be defined, etc.
- **Formats** — Duration strings (e.g., `5m`, `30s`), regex patterns
- **Constraints** — Positive integers for `max_workers`, `concurrency`, etc.

Additional validation happens at runtime via `apiary validate`:
- Removed directives (deprecated features)
- Unknown fields (typos in config)
- Workflow-specific authoring rules (v2)

## Troubleshooting

### Schema Not Showing in VS Code

1. Reload VS Code (`Cmd+Shift+P` → "Reload Window")
2. Verify `.vscode/settings.json` exists and is valid JSON
3. Check YAML extension is installed and enabled
4. Ensure file is named `apiary.yaml` (case-sensitive)

### Autocomplete Not Working

- Reload VS Code
- Check that you're editing `apiary.yaml` (not a copy)
- Verify YAML extension is active (check Extensions panel)

### Pre-commit Hook Not Running

```bash
# Check hook is executable
ls -la .git/hooks/pre-commit

# Make it executable if needed
chmod +x .git/hooks/pre-commit

# Test manually
.git/hooks/pre-commit
```

### "apiary CLI not found"

The hook gracefully skips validation if `apiary` is not in your PATH. To enable validation:

```bash
# Install the CLI
go install github.com/orlandoburli/apiary/cmd/apiary@latest

# Verify it's in PATH
which apiary
```

## Reference

- **Schema file**: `schema/apiary.json` (JSON Schema draft-07)
- **Validation code**: `src/internal/config/validate.go`
- **Linting**: `src/internal/config/lint.go`
- **Workflow rules**: `src/internal/config/workflow_validate.go`
