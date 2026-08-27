# Plugin directory

Plugins extend Apiary without a fork, as separate executables speaking
[protocol 1](plugins.md#protocol-version-1). This page lists the ones we know
about.

Apiary has **no plugin registry**. Nothing here is downloaded, verified, or
endorsed by the daemon — a listing is a pointer to someone else's repository,
and installing a plugin means placing files you obtained and checked yourself.
Read [Installing a plugin](plugins.md#installing-a-plugin) before you add any of
them, and prefer a release whose checksum you can pin in the manifest.

## Available plugins

| Plugin | Capability | What it does | Home |
|---|---|---|---|
| `dev.apiary.routines` | `source` | **Scheduled routines.** Turns each due cron occurrence into a work item, so a nightly audit or an hourly sweep routes through triggers and workflows like any other task. | [apiary-routines](https://github.com/orlandoburli/apiary-routines) · [docs](https://orlandoburli.com.br/apiary-routines/) |

## Reference plugins

These ship inside this repository under `src/examples/plugins/`, as working
starting points rather than things to run in production. The three source
plugins are behaviourally identical on purpose — diff them to see one contract
expressed in three ecosystems.

| Plugin | Language | Demonstrates |
|---|---|---|
| `event-file` | Go (SDK) | An `event_exporter` appending one redacted event per line |
| `source-file` | Go (SDK) | A `source` polling work items from a JSON file |
| `source-bash` | Bash + jq | The same source as a shell script — no build step |
| `source-node` | TypeScript | The same source compiled to a shebang executable |

## Writing one

Start from the [Plugin SDK](plugin-sdk.md) page, or copy the reference plugin
closest to your language. A plugin is one executable plus one
`apiary-plugin.json` manifest; the daemon spawns it per call, writes one JSON
request to stdin and reads one response from stdout.

Two things worth knowing before you start, both learned from building
`dev.apiary.routines`:

- **The Go SDK is not currently importable from outside this repository** —
  `go get github.com/orlandoburli/apiary/sdk/plugin` does not resolve
  ([#434](https://github.com/orlandoburli/apiary/issues/434)). Until that is
  fixed, a third-party Go plugin implements the (small) wire protocol directly.
  Every other language was always going to do that anyway.
- **`config_schema` supports a subset of JSON Schema** and fails closed on
  anything outside it — see the keyword list in
  [Overview & Architecture](plugins.md#manifest-version-1). An unsupported keyword such as
  `pattern` invalidates the whole manifest, and the plugin is then reported as
  *not installed* rather than as having a bad schema.

## Getting listed

Open a PR adding a row to the table above. Include the plugin id, its
capabilities, one sentence on what it does, and a link to its repository. The
plugin must be publicly readable so operators can inspect what they are about to
run as the daemon's user.
