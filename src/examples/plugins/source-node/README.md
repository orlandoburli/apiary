# Source-node reference plugin (TypeScript)

The same behavior as the Go [`source-file`](../source-file/README.md) plugin —
poll Apiary work items from a JSON file — implemented in **TypeScript** and run
with Node, to show plugins can be written in any ecosystem. No runtime
dependencies; TypeScript is a dev-time dependency only. Requires `node` on the
daemon host.

Build (compiles `main.ts` and prepends a `#!/usr/bin/env node` shebang so the
output is directly executable):

```bash
cd src/examples/plugins/source-node
npm install
npm run build          # produces ./apiary-plugin-source-node
```

Install:

```bash
mkdir -p .apiary/plugins/dev.apiary.source-node
cp apiary-plugin.json apiary-plugin-source-node .apiary/plugins/dev.apiary.source-node/
apiary plugins validate
```

Enable and bridge it in `apiary.yaml` (absolute `path` — plugin processes run
with cwd set to their install directory):

```yaml
plugins:
  - id: dev.apiary.source-node
    timeout: 5s
    config:
      path: /path/to/project/.apiary/incoming-items.json

sources:
  - id: node-items
    type: plugin
    poll_interval: 30s
    config:
      plugin: dev.apiary.source-node
```

Test it without Apiary — the protocol is pipeable:

```bash
echo '{"protocol":1,"request_id":"t1","capability":"source","method":"poll","config":{"path":"items.json"}}' \
  | ./apiary-plugin-source-node
```
