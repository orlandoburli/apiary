# Source-file reference plugin

This minimal protocol-1 `source` plugin polls Apiary work items from a JSON
file. Any external process can append items to the file and Apiary dispatches
matching workflows on the next poll — the simplest possible custom source.

Build and install it from the repository root:

```bash
go build -o src/examples/plugins/source-file/apiary-plugin-source-file ./src/examples/plugins/source-file
mkdir -p .apiary/plugins/dev.apiary.source-file
cp src/examples/plugins/source-file/apiary-plugin.json \
  src/examples/plugins/source-file/apiary-plugin-source-file \
  .apiary/plugins/dev.apiary.source-file/
apiary plugins validate
```

Then enable it and bridge a source to it in `apiary.yaml`:

```yaml
plugins:
  - id: dev.apiary.source-file
    timeout: 5s
    config:
      path: /path/to/project/.apiary/incoming-items.json

sources:
  - id: file-items
    type: plugin
    poll_interval: 30s
    config:
      plugin: dev.apiary.source-file
```

!!! note
    Use an absolute `path`. Plugin processes run with their working directory
    set to the plugin's install directory (not the project root), so a
    relative path would resolve inside `.apiary/plugins/dev.apiary.source-file/`
    — and an absent file there reads as "no work yet".

The items file holds a JSON array in the SDK wire shape
(`sdk/plugin/source.go`); IDs are the dedup key, so keep them stable:

```json
[
  {
    "id": "task-001",
    "title": "Investigate checkout latency",
    "description": "p99 above 2s since the 14:00 deploy.",
    "labels": ["team:payments", "severity:high"],
    "state": "open"
  }
]
```
