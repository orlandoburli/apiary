# Out-of-process plugins

Apiary protocol 1 runs third-party extensions as child processes instead of
loading Go binaries. A plugin crash, invalid response, or timeout terminates only
that invocation; it does not crash the dispatcher. Plugins are never executed
during discovery or configuration validation.

## Install and enable

An installed plugin is a directory containing `apiary-plugin.json` and the
executable named by that manifest. The default search directory is
`.apiary/plugins` beside `apiary.yaml`; `plugin_dirs` overrides it.

```yaml
plugin_dirs:
  - .apiary/plugins
  - ~/.local/share/apiary/plugins

plugins:
  - id: dev.apiary.event-file
    enabled: true                 # omitted means true
    timeout: 5s                   # default: 10s, per invocation
    config:
      path: .apiary/events.jsonl
```

Use `apiary plugins`, `apiary plugins inspect <id>`, and
`apiary plugins validate` to inspect installations. `apiary validate` also
discovers every enabled plugin and validates its configuration against the
manifest's JSON Schema. Set `enabled: false` to keep an installed/configured
plugin available without starting or schema-validating it.

Relative plugin directories resolve beside `apiary.yaml`. Discovery is
deterministic and rejects duplicate IDs rather than choosing one by path order.

## Manifest version 1

```json
{
  "schema_version": 1,
  "id": "dev.apiary.event-file",
  "version": "1.0.0",
  "apiary": ">= 0.10.0-0",
  "protocol": 1,
  "executable": "apiary-plugin-event-file",
  "capabilities": ["event_exporter"],
  "config_schema": {
    "type": "object",
    "properties": {"path": {"type": "string", "minLength": 1}},
    "required": ["path"],
    "additionalProperties": false
  },
  "security": {
    "network": false,
    "read_paths": [],
    "write_paths": ["configured event file"],
    "secret_env": []
  }
}
```

- `id` is a lowercase reverse-DNS identifier and is stable across releases.
- `version` follows semantic versioning; `apiary` is a semantic-version constraint.
- `protocol` must be `1`. Unknown manifest/protocol versions fail closed.
- `executable` is relative to the plugin directory. Absolute paths, traversal,
  symlinks, non-regular files, and non-executable files are rejected.
- `checksum` (optional) pins the SHA-256 of the executable, as `sha256:<hex>` or
  bare hex. When present it is verified before the plugin is loaded and again
  before each invocation, so a binary replaced after installation is rejected.
  A malformed value is an error rather than being treated as unpinned, so
  `apiary validate` catches a bad pin. This is **tamper-evidence, not
  authenticity**: the digest lives beside the binary, so anyone able to rewrite
  the executable can rewrite the pin too. It detects accidental drift and
  unsophisticated swaps, not a determined attacker with write access.
- `capabilities` accepts `source`, `runner`, `workflow_action`,
  `approval_provider`, `secret_provider`, and `event_exporter`.
- `config_schema` supports `type`, `properties`, `required`,
  `additionalProperties`, `enum`, `items`, `minLength`, `maxLength`, `minimum`,
  `maximum`, `minItems`, and `maxItems`. Unknown validation keywords fail closed.
- `security` is an inspectable declaration, not an OS sandbox guarantee.

## Protocol version 1

Apiary starts a fresh process for each invocation, writes one compact JSON object
plus a newline to stdin, and expects one JSON response on stdout. Diagnostic
output belongs on stderr. Stdout is limited to 4 MiB and captured stderr to 64
KiB. The per-instance deadline cancels and terminates the child process.

Request:

```json
{
  "protocol": 1,
  "request_id": "1784736000000000000",
  "capability": "event_exporter",
  "method": "export",
  "config": {"path": ".apiary/events.jsonl"},
  "payload": {"schema_version": 1, "type": "runner.started"}
}
```

Success and failure responses echo the request ID:

```json
{"protocol":1,"request_id":"1784736000000000000","result":{"written":true}}
{"protocol":1,"request_id":"1784736000000000000","error":{"code":"write_failed","message":"permission denied"}}
```

Capability method vocabulary:

| Capability | Protocol methods |
|---|---|
| `source` | `connect`, `poll`, `acknowledge`, `write_result`, plus optional source capabilities |
| `runner` | `configure`, `run` |
| `workflow_action` | `run` |
| `approval_provider` | `notify`, `remind`, `escalate` |
| `secret_provider` | `resolve` |
| `event_exporter` | `export` |

Protocol envelopes are stable; capability payloads use the corresponding
versioned Apiary internal contract. The first runtime integration is
`event_exporter`. The remaining capability names reserve the same manifest and
transport boundary for their proxies without requiring a new installation model.

Go plugin authors can import `github.com/orlandoburli/apiary/sdk/plugin` and call
`plugin.Main(handler)`. The SDK decodes one request, rejects protocol mismatches,
echoes the request ID, and emits the structured response envelope. Plugins in
other languages can implement the JSON examples above directly.

## Trust and secrets

Plugins execute with the Apiary service account's OS permissions. Before install:

1. Obtain the binary from a trusted publisher.
2. Verify its checksum and signature out of band.
3. Inspect the manifest's network, path, and secret requirements.
4. Install into a directory writable only by the Apiary operator/service account.

Apiary does not download or auto-upgrade plugins. Upgrade atomically by verifying
the new artifact in a staging directory, stopping Apiary, replacing the complete
plugin directory, running `apiary plugins validate`, and restarting. Keep the old
directory for rollback, but never leave two versions with the same ID in searched
directories.

Child processes receive a minimal environment (`PATH`, home/temp/locale/timezone)
plus only variables listed in `security.secret_env`. Secret values are never
included in protocol requests or error messages. Plugin configuration should
contain references or non-secret options, not credentials. Event exporters
receive the persisted event after Apiary's metadata redaction.

Process isolation is not a security sandbox: a malicious executable can still use
the service account's filesystem and network access. Use OS-level sandboxing,
containers, restricted service users, and egress controls where the trust level
requires them.

## Reference plugin

`src/examples/plugins/event-file` demonstrates an `event_exporter` that appends one
redacted event JSON object per line. Its README contains build and installation
commands.
