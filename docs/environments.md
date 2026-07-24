# Environments, Promotion, and Rollback

Apiary supports named **environments** (e.g. `development`, `staging`, `production`) declared directly in `apiary.yaml`. Each environment is a set of overlays applied on top of the base configuration — no separate files needed, no duplicated workflow definitions.

## Declaring environments

Add an `environments:` block to `apiary.yaml`:

```yaml
environments:
  development:
    settings:
      concurrency: 2
    sources:
      - id: github
        config:
          endpoint: ${DEV_GITHUB_ENDPOINT}
    enabled_sources:
      - github
    agents:
      - id: engineer
        env:
          GH_TOKEN: ${DEV_GH_TOKEN}

  staging:
    settings:
      concurrency: 4
    sources:
      - id: github
        config:
          endpoint: ${STAGING_GITHUB_ENDPOINT}

  production:
    settings:
      concurrency: 8
      log_level: warn
```

## Overlay precedence

When an environment is active, overlays are applied in this order (highest to lowest priority):

1. **Environment overlay** — values declared under `environments.<name>`.
2. **Base config** — the `apiary.yaml` top-level values.

Within an overlay, only fields that are explicitly set take effect. Absent fields are inherited from the base config.

## Overlay fields

### `settings`

| Field | Effect |
|---|---|
| `concurrency` | Overrides `settings.concurrency` |
| `log_level` | Overrides `settings.log_level` |
| `max_attempts` | Overrides `settings.max_attempts` |

### `sources[]`

Each entry is matched by `id`. The following fields are merged:

- `config`: merged key-by-key into the base source's config map. Overlay keys win; absent keys are inherited.
- `poll_interval`: replaces the base source's poll interval.

### `agents[]`

Each entry is matched by `id`. The following fields are overridden when non-zero:

- `model`: replaces the agent's model.
- `runner`: replaces the agent's runner.
- `max_workers`: replaces the agent's max_workers.
- `env`: merged key-by-key into the agent's env map. Overlay keys win.

### `runners[]`

Each entry is matched by `id`. The following fields are merged:

- `config`: merged key-by-key into the runner's config map.

### `enabled_sources`

When set, restricts the active source set to this list. Sources not listed are removed from the resolved config. Useful for environment-specific data isolation.

### `rollout`

Restricts dispatch to tasks matching all non-empty criteria (AND semantics):

```yaml
rollout:
  sources: [github]      # only from these source IDs
  labels: [ready]         # only tasks with at least one of these labels
  percentage: 10          # only 10% of eligible tasks
```

## Secret references

**Never store secret values directly in `apiary.yaml`.** Use `${VAR}` references to environment variables loaded from `.env`:

```yaml
environments:
  staging:
    agents:
      - id: engineer
        env:
          GH_TOKEN: ${STAGING_GH_TOKEN}
```

The `.env` file beside `apiary.yaml` is loaded automatically. Values set in the shell take precedence over `.env` entries.

## Running with an environment

```sh
apiary run --env staging
apiary run --env production --profile fast
```

## Validating environments

```sh
# Validate base config only (current behaviour)
apiary validate

# Validate a specific environment
apiary validate --env staging

# Validate base config and all declared environments
apiary validate --all-envs
```

## Comparing environments

```sh
# Compare base config vs staging
apiary diff base staging

# Compare staging vs production
apiary diff staging production
```

The diff shows:
- Added / removed sources, agents, runners, workflows
- Changed agent model, runner, env keys
- Changed source config keys
- Changed settings (concurrency, log_level)
- Config digest for both sides

## Promoting a configuration

`apiary promote` validates the source environment and records an auditable entry in the local database. It does **not** modify `apiary.yaml` — promotion is purely an audit event.

```sh
# Promote base config to staging (validate before promoting)
apiary promote base staging

# Promote staging to production with a note
apiary promote staging production --note "release v2.4.0"

# Skip validation (e.g. already validated in CI)
apiary promote staging production --skip-validate
```

### What gets recorded

| Field | Value |
|---|---|
| `environment` | The target environment name |
| `config_digest` | SHA-256 of the effective (post-overlay) config YAML |
| `git_revision` | `git rev-parse HEAD` at promotion time (when available) |
| `event` | `"promote"` |
| `from_environment` | The source environment name |
| `note` | Optional operator note |

## Rollback

`apiary rollback` inspects the `config_revisions` table and helps you restore a previous configuration.

```sh
# List the last 20 recorded revisions for staging
apiary rollback --env staging --list

# Record a rollback intent to a specific digest prefix
apiary rollback --env staging --to abc123def --note "reverting to pre-deploy state"
```

**Rollback does not modify `apiary.yaml`.** It records the intent in the audit table and prints the git revision so you can restore the YAML yourself:

```sh
git checkout <git_revision> -- apiary.yaml
```

## Audit trail

Every attempt records the effective configuration in the `workflow_instances` table:

| Column | Description |
|---|---|
| `config_digest` | SHA-256 hex of the resolved config at dispatch time |
| `git_revision` | git HEAD at daemon startup |
| `environment` | Active environment name (empty = base config) |

Promotion and rollback events are stored in `config_revisions` (queryable via SQLite at `.apiary/apiary.db`).
