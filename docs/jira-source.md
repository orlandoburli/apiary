# Jira Source

The `jira` source adapter polls issues from a **Jira Cloud** site via JQL
search and maps them to Apiary tasks. Jira Server / Data Center is not
supported (the adapter relies on Cloud-only API endpoints and auth).

## Configuration

```yaml
sources:
  - id: my-jira
    type: jira
    config:
      base_url: https://yoursite.atlassian.net
      email: bot@example.com
      api_token: ${JIRA_API_TOKEN}
      project: ERP                    # optional
      started_state: In Progress      # optional
    poll_interval: 120s
    filters:
      jql: 'labels = apiary AND statusCategory != Done'
      states: [to do, in progress]
      labels: [apiary]
```

### `config` fields

| Field | Required | Description |
|---|---|---|
| `base_url` | yes | Jira Cloud site URL (`https://<site>.atlassian.net`) |
| `email` | yes | Atlassian account email for Basic auth |
| `api_token` | yes | API token — create one at [id.atlassian.com](https://id.atlassian.com/manage-profile/security/api-tokens) |
| `project` | no | Project key (e.g. `ERP`) to scope polling; recommended |
| `started_state` | no | Status name Acknowledge transitions issues to on dispatch |

If neither `project` nor `filters.jql` is set, the adapter polls every issue
visible to the API user and logs a warning.

### `filters`

| Field | Description |
|---|---|
| `jql` | Any JQL clause, ANDed into every poll's search — the primary filter for Jira sources |
| `states` | Only ingest issues in these statuses (matched by status **name**, case-insensitive, client-side) |
| `labels` | Only ingest issues carrying all of these labels (client-side) |

## Behavior

- **JQL search.** Polling uses the enhanced `/rest/api/3/search/jql`
  endpoint, combining `project`, your `filters.jql`, and an incremental
  `updated >=` cut-off. Bare JQL datetimes are interpreted in the API user's
  profile timezone, which the adapter resolves once from `/myself` (falling
  back to UTC — a small overlap window plus client-side filtering keeps
  results exact either way).
- **Human references.** Issues are tracked by their immutable numeric id but
  displayed with the issue key (e.g. `ERP-42`) in the dashboard's `#` column;
  item URLs open the issue in the Jira web UI (`o` in the dashboard).
- **Acknowledge transitions the issue.** On dispatch the issue moves to
  `started_state` if configured, otherwise to the first transition whose
  target sits in Jira's *In Progress* status category — so the board reflects
  pickup, like the Plane adapter.
- **Comments are rich text.** Run results are posted as native Jira (ADF)
  comments: status line, detected PR link, output as a code block. Issue
  descriptions and comments are flattened to plain text for agents and
  approval matching.
- **Approvals work.** The adapter implements per-task polling, so approval
  steps can match human Jira comments, labels, or statuses.
- **Labels.** Adding/removing labels uses Jira's atomic update verbs; labels
  are created implicitly. Jira labels cannot contain spaces — names with
  whitespace are rewritten with `-` (e.g. `needs review` → `needs-review`).
- **No CI polling.** `wait_for` steps with `kind: ci` need a source that can
  see pull requests; Jira issues have no PR mapping, so host CI waits on a
  GitHub source instead.

## State names in hooks

`set_state` and `started_state` values are matched against the target status
of the issue's *available transitions* (case-insensitive; the transition's
own name works as a fallback), so hooks read naturally:

```yaml
workflows:
  - id: implement
    trigger:
      priority: 10
      match:
        source: my-jira
        labels: [agent:engineer]
    steps:
      - id: run
        agent: engineer
    on_complete:
      set_state: in review
```

Jira only offers transitions that are valid from the issue's current status.
If the target status is unreachable, the adapter fails with an error listing
the transitions that *were* available — fix the Jira workflow or pick a
reachable status.

## Per-agent identity

An agent's `source_token` override can carry either just an API token (the
source's `email` is reused) or a full `email:api_token` pair to post
comments and transitions as a different Atlassian account.

## Rate limits

Jira Cloud rate-limits per user and answers `429` with a `Retry-After`
header, which the adapter honours with exponential backoff. With several
Jira sources or short intervals, keep `poll_interval: 60s` or higher.
