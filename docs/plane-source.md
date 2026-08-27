# Plane Source

The `plane` source adapter polls work items from a
[Plane](https://plane.so) project — cloud or self-hosted — and maps them to
Apiary tasks.

## Configuration

```yaml
sources:
  - id: project-erp
    type: plane
    config:
      workspace: my-workspace
      project: 6ee18c57-7f88-414c-bad5-44ef4afd979b
      api_key: ${PLANE_API_KEY}
      base_url: https://plane.example.com    # self-hosted instance
    poll_interval: 60s
    filters:
      states: [backlog, todo, in progress]
```

### `config` fields

| Field | Required | Description |
|---|---|---|
| `workspace` | yes | Workspace slug |
| `project` | yes | Project UUID |
| `api_key` | yes | Plane API key |
| `base_url` | no | Instance URL for self-hosted Plane (defaults to Plane cloud) |

These are the only keys accepted; `apiary validate` rejects anything else (see
[Accepted `config` keys](configuration.md#accepted-config-keys)).

### `filters`

| Field | Description |
|---|---|
| `states` | Only poll items in these states (matched by state **name**, e.g. `backlog`, `todo`, `in progress`) |
| `labels` | Only poll items carrying these labels |

## Behavior

- **Lazy metadata.** On the first poll the adapter loads the project's
  states and labels once and resolves names ↔ IDs from then on, spacing the
  calls to respect Plane's rate limits. Config errors (bad key, wrong
  workspace/project) surface on connect; metadata errors on first poll.
- **Human references.** Work items are tracked with their project reference
  (e.g. `ERP-42`), which is what the dashboard's `#` column shows, and item
  URLs open the Plane web UI (`o` in the dashboard).
- **Write operations** mirror the GitHub adapter: post the result as a
  comment, transition state (`on_complete.set_state` takes a state *name*),
  and add labels (labels are created in the project if they don't exist
  yet).

## State names in hooks

`set_state` values are matched against the project's state names, so hooks
read naturally:

```yaml
workflows:
  - id: implement
    trigger:
      priority: 10
      match:
        source: project-erp
        labels: [agent:engineer]
    steps:
      - id: run
        agent: engineer
    on_complete:
      set_state: in review
```

## Rate limits

Self-hosted and cloud Plane instances enforce per-minute API budgets. If you
run several Plane sources or a short `poll_interval`, space the polling
(`poll_interval: 60s` or higher) — the adapter already paces its own
metadata calls.
