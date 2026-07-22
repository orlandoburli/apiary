# Event-file reference plugin

This minimal protocol-1 `event_exporter` appends each persisted, redacted Apiary
execution event to a JSON Lines file.

Build and install it from the repository root:

```bash
go build -o src/examples/plugins/event-file/apiary-plugin-event-file ./src/examples/plugins/event-file
mkdir -p .apiary/plugins/dev.apiary.event-file
cp src/examples/plugins/event-file/apiary-plugin.json \
  src/examples/plugins/event-file/apiary-plugin-event-file \
  .apiary/plugins/dev.apiary.event-file/
apiary plugins validate
```

Then enable it in `apiary.yaml`:

```yaml
plugins:
  - id: dev.apiary.event-file
    timeout: 5s
    config:
      path: .apiary/events.jsonl
```

The plugin intentionally has no network access requirement and requests write
access only to the configured event file. It is a protocol example, not a log
rotation or delivery-retry service.
