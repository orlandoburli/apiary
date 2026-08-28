# Plugin directory

Plugins extend Apiary without a fork, as separate executables speaking
[protocol 1](plugins.md#protocol-version-1). The **registry** is how you find
and install them from the command line:

```bash
apiary plugins search cron
apiary plugins info dev.apiary.routines
apiary plugins install dev.apiary.routines
```

A listing is a pointer to someone else's repository. It is reviewed; it is not
endorsed, and Apiary has not read the code. The registry holds **metadata and
digests only** — artifacts stay on their publisher's release infrastructure, and
the daemon never contacts a registry at all. A plugin you install runs
unsandboxed, with the daemon's OS permissions, as its user: read it before you
enable it.

## What the registry does for you

Installing by hand is still supported and still documented
([Installing a plugin](plugins.md#installing-a-plugin)). What the registry adds
is the two steps a human does badly:

- **Picking the right artifact.** The host-version constraint, the protocol
  version, the platform and any withdrawal are resolved *before* a byte is
  downloaded — so "0.3.0 requires apiary >= 0.20.0, you are on 0.19.1" is an
  answer you get up front rather than an error after unpacking.
- **Checking it is the one the publisher shipped.** The archive's digest is
  verified before it is unpacked, the manifest inside is checked against the
  listing, and the executable's digest is written into the installed manifest as
  a `checksum` pin. That pin comes from the registry repository rather than from
  beside the binary, which is what turns it from drift detection into a
  supply-chain check — see [Trust and secrets](plugins.md#trust-and-secrets).

Everything else stays as it was. Installation makes a plugin *available*; only a
`plugins:` entry in `apiary.yaml` makes it *run*, and the daemon has to be
restarted. `apiary plugins install` never edits your config.

## Available plugins

| Plugin | Capability | What it does | Home |
|---|---|---|---|
| `dev.apiary.routines` | `source` | **Scheduled routines.** Turns each due cron occurrence into a work item, so a nightly audit or an hourly sweep routes through triggers and workflows like any other task. | [apiary-routines](https://github.com/orlandoburli/apiary-routines) · [docs](https://orlandoburli.com.br/apiary-routines/) |

`apiary plugins search` is the live version of this table, and
`apiary plugins info <id>` adds what CI observed: whether the release passed the
[conformance kit](plugin-sdk.md#the-conformance-kit), which platforms it builds
for, and whether it can be installed on *this* host.

## Reference plugins

These ship inside this repository under `src/examples/plugins/`, as working
starting points rather than things to run in production. They publish no
release artifacts, so they are not in the registry — build them from source. The
three source plugins are behaviourally identical on purpose — diff them to see
one contract expressed in three ecosystems.

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

One thing worth knowing before you start, learned from building
`dev.apiary.routines`: **`config_schema` supports a subset of JSON Schema** and
fails closed on anything outside it — see the keyword list in
[Overview & Architecture](plugins.md#manifest-version-1). An unsupported keyword
such as `pattern` invalidates the whole manifest, and the plugin is then
reported as *not installed* rather than as having a bad schema.

Go authors can `go get github.com/orlandoburli/apiary/sdk` — the SDK is its own
module, tagged independently of the daemon.

## Getting listed

Open a pull request adding `registry/plugins/<your-plugin-id>.yaml`, described
in full in [Publishing to the registry](plugin-sdk.md#publishing-to-the-registry).
The plugin must be publicly readable so operators can inspect what they are
about to run as the daemon's user, and every release must declare both digests —
there is no unpinned listing.

CI does not take any of it on trust: it downloads each artifact, re-derives both
digests, cross-checks the manifest inside the archive against your entry, and
runs the conformance kit. A listing cannot claim a digest, a compatibility
range, or a conformance verdict that CI could not reproduce.

## Turning the registry off

The registry is a CLI convenience, not a dependency. To disable it — and keep
manual installation only:

```yaml
plugin_registries: []
```

To resolve against an internal mirror instead, or to pin a registry to a signing
key, see [Registries and mirrors](plugins.md#registries-and-mirrors).
