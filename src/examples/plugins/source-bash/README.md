# Source-bash reference plugin

The same behavior as the Go [`source-file`](../source-file/README.md) plugin —
poll Apiary work items from a JSON file — implemented as a **Bash script**, to
show that a plugin is just an executable speaking one JSON request/response on
stdin/stdout. Requires `bash` and `jq` on the daemon host.

There is nothing to build. Install by copying the script and manifest:

```bash
mkdir -p .apiary/plugins/dev.apiary.source-bash
cp src/examples/plugins/source-bash/apiary-plugin.json \
   src/examples/plugins/source-bash/apiary-plugin-source-bash \
   .apiary/plugins/dev.apiary.source-bash/
chmod +x .apiary/plugins/dev.apiary.source-bash/apiary-plugin-source-bash
apiary plugins validate
```

Enable and bridge it in `apiary.yaml` (absolute `path` — plugin processes run
with cwd set to their install directory):

```yaml
plugins:
  - id: dev.apiary.source-bash
    timeout: 5s
    config:
      path: /path/to/project/.apiary/incoming-items.json

sources:
  - id: bash-items
    type: plugin
    poll_interval: 30s
    config:
      plugin: dev.apiary.source-bash
```

Test it without Apiary — the protocol is pipeable:

```bash
echo '{"protocol":1,"request_id":"t1","capability":"source","method":"poll","config":{"path":"items.json"}}' \
  | ./apiary-plugin-source-bash
```
