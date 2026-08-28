# Design: plugin registry and command-line installs

## Registry shape

The registry is a **static, PR-reviewed index**, not a service. It lives in this
repository and is compiled by CI into JSON published beside the docs site.

```
registry/
├── plugins/
│   ├── dev.apiary.routines.yaml     ← one file per plugin id
│   └── dev.apiary.source-file.yaml
└── schema/
    └── index-v1.json                ← JSON Schema for the compiled index
```

There are no accounts, no uploads and no runtime API. Adding or updating a
plugin is a pull request; the review is the trust boundary and the CI job is
the gate.

### Entry format

```yaml
schema_version: 1
id: dev.apiary.routines
summary: Turns each due cron occurrence into a work item.
capabilities: [source]
homepage: https://orlandoburli.com.br/apiary-routines/
repository: https://github.com/orlandoburli/apiary-routines
license: MIT
releases:
  - version: 0.1.0
    apiary: ">= 0.18.0-0"     # semver constraint, matches the manifest's field
    protocol: 1
    published_at: 2026-08-27
    artifacts:
      - os: darwin
        arch: arm64
        url: https://github.com/orlandoburli/apiary-routines/releases/download/v0.1.0/apiary-routines_0.1.0_darwin_arm64.tar.gz
        archive_sha256: "…"        # of the downloaded file
        executable_sha256: "…"     # of the executable inside, after unpacking
```

Rules the schema enforces:

- `id` is reverse-DNS and matches `validPluginID` in
  `src/internal/plugin/manifest.go`, and must equal the file's basename.
- `version` is semver; versions are unique per entry and never rewritten. A bad
  release is marked `yanked: true` with a `yanked_reason`, never deleted — the
  index has to stay honest about what was once resolvable.
- `capabilities` uses the same vocabulary as the manifest.
- `protocol` must be a protocol the CLI supports (currently `1`).
- `archive_sha256` and `executable_sha256` are required; there is no unpinned
  release. An artifact whose digest cannot be reproduced by CI is not listed.
- `os`/`arch` use Go's `GOOS`/`GOARCH` spelling. A release with no artifact for
  the running platform is resolvable but not installable, and says so.

### Compiled index

CI concatenates the entries into a single document, so the CLI makes one
request:

```json
{
  "schema_version": 1,
  "generated_at": "2026-08-27T00:00:00Z",
  "plugins": [ { "id": "…", "releases": [ … ], "conformance": { … } } ]
}
```

`conformance` is written by CI, not by the submitter: the kit's verdict, the kit
version and the commit it ran at. It is displayed by `plugins info` and is
never presented as an endorsement.

### Registry CI

For every changed entry:

1. Validate against `schema/index-v1.json`; check id/basename agreement,
   semver, capability names, protocol.
2. Download each artifact; assert `archive_sha256` matches.
3. Unpack into a temp dir; assert the embedded `apiary-plugin.json` parses and
   its `id`, `version`, `protocol` and `capabilities` match the entry, and that
   `executable_sha256` matches the executable it names.
4. Run the conformance kit (the harness behind
   `.github/workflows/plugin-conformance.yml`) against the artifact for the CI
   platform; record the verdict.
5. On merge to `main`, rebuild `index.json`, sign it (phase 3) and deploy with
   the docs site.

Steps 2–4 mean a listing cannot claim a digest, a compatibility range or a
conformance result that CI could not reproduce.

## Resolution

`resolve(id, constraint)` runs entirely against the cached index:

1. Look up the plugin id; unknown ids fail with the closest matches by edit
   distance.
2. Filter releases: not `yanked`, `protocol` supported by this CLI, `apiary`
   constraint satisfied by the running version (the same semver evaluation
   `Installed.Validate` uses today, so the pre-flight answer and the
   post-install answer cannot disagree), and an artifact for `runtime.GOOS` /
   `runtime.GOARCH`.
3. Pick the highest remaining semver, or the exact version when `id@version`
   was given.
4. If the filter empties, report *which* predicate eliminated the newest
   candidate — "0.3.0 requires apiary >= 0.20.0, you are on 0.19.1" is the
   whole point of resolving before downloading.

### Index cache

Cached under `${XDG_CACHE_HOME:-~/.cache}/apiary/registry/<host>/index.json`
with its ETag. Refresh is a conditional GET, at most once per command, with a
30s timeout. `--offline` uses the cache and never touches the network; a cold
cache under `--offline` is a clear error, not an empty result.

## Install state machine

`apiary plugins install <id>[@version]` — every step is reversible until the
last one, which is atomic:

| # | Step | Failure behaviour |
|---|---|---|
| 1 | Resolve from the index | Abort; nothing on disk |
| 2 | Refuse if the id is already installed in any `plugin_dirs` entry | Abort, pointing at `upgrade` |
| 3 | Download to `TMPDIR/apiary-install-*/archive` with a size cap and a deadline | Abort; temp dir removed |
| 4 | Verify `archive_sha256` (`fileSHA256`) | Abort loudly — a digest mismatch is reported as a supply-chain failure, not a network hiccup |
| 5 | Unpack into the staging dir, rejecting absolute paths, `..`, symlinks, devices and hardlinks | Abort |
| 6 | `plugin.Load(stagingDir, version)` + `Installed.Validate` — manifest schema, id agreement with the requested id, protocol, executable safety | Abort with the validator's own message |
| 7 | Verify `executable_sha256` against the named executable | Abort loudly |
| 8 | Inject `checksum: sha256:<executable_sha256>` if the manifest has none; verify agreement if it has one | A publisher pin that disagrees with the index aborts the install and is a registry bug |
| 9 | Print the trust summary; prompt unless `--yes` | Abort; temp dir removed |
| 10 | `rename(stagingDir, <plugin_dir>/<id>)` | Cross-device fallback: copy into `<plugin_dir>/.<id>.incoming`, then rename within the target filesystem |
| 11 | Re-run discovery over the target directory and print the `plugins:` snippet | Reports a broken install rather than claiming success |

Nothing in this sequence executes plugin code. The first execution is still the
daemon's first invocation, after the operator has written a `plugins:` entry and
restarted.

### Trust summary

Printed at step 9, always — `--yes` suppresses the prompt, not the output:

```
dev.apiary.routines 0.1.0  (source)
  from   https://github.com/orlandoburli/apiary-routines/releases/download/v0.1.0/…tar.gz
  sha256 3f9a…c21e (verified)
  conformance  passed (kit 1.0.0, 2026-08-27)

  Declared access (a declaration, not a sandbox):
    network      yes
    read paths   ~/.apiary/routines.yaml
    write paths  (none)
    secret env   (none)

  This executable will run with the daemon's OS permissions, as its user.
  A registry listing is a pointer to someone else's repository — it is not an
  endorsement, and Apiary has not reviewed this code.

Install into .apiary/plugins? [y/N]
```

### Pin injection

Step 8 is the security argument for the whole change. Today `docs/plugins.md`
correctly describes the manifest `checksum` as tamper-evidence only: the pin
sits in the same directory as the binary it protects. After a registry install
the digest originates in the registry repository — a different host, a
different access-control domain, reviewed in a pull request and re-derived by
CI — so `integrityGuard.check` on every invocation is now comparing the file
against a value the plugin's publisher cannot silently rewrite.

The manifest rewrite is minimal: decode, set `checksum` when absent, re-encode
with stable key order, write inside the staging dir before step 10. When the
publisher already pinned, the installer verifies agreement and leaves the file
byte-identical.

## Upgrade and uninstall

`upgrade <id>` runs steps 1–9 into a staging dir, then swaps:

1. `rename(<plugin_dir>/<id>, <plugin_dir>/<id>.bak)` (one generation only; a
   previous `.bak` is removed first).
2. `rename(staging, <plugin_dir>/<id>)`.
3. Re-run discovery; on failure, restore from `.bak` and exit non-zero.

The daemon is not touched, and the command says so: a running daemon keeps the
old plugin until it is restarted. `--rollback` restores `.bak`.

`uninstall <id>` removes the directory, and refuses while the id appears as an
enabled entry in `apiary.yaml` (`--force` overrides). It never edits config,
because a `plugins:` entry pointing at an uninstalled id is already a clear
validation error rather than a silent one.

## Configuration

```yaml
plugin_registries:
  - https://orlandoburli.com.br/apiary/registry/v1/index.json
  # - file:///opt/apiary/registry/index.json     # internal mirror
```

Unset means the official index. `[]` disables every registry subcommand with an
explicit message. Entries are consulted in order and the first hit wins;
`--registry` overrides the list for one command. Only `https://` and `file://`
are accepted — `http://` is rejected outright, since digests protect the payload
but not the resolution.

The daemon ignores this field entirely; it is CLI-only configuration that lives
in `apiary.yaml` for the same reason `plugin_dirs` does.

## Signing (phase 3)

CI signs `index.json` with minisign; the public key is compiled into the
binary. The CLI verifies before parsing and fails closed — no
`--insecure-skip-signature`. Mirrors either carry the upstream signature
verbatim (a `file://` copy of the signed document verifies exactly like the
original) or are configured with their own key via
`plugin_registries[].public_key`.

Signature verification covers the index, which covers the digests, which cover
the artifacts. It does not authenticate plugin *publishers*: signing the
artifacts themselves is a separate change, and its absence is stated on the
docs page rather than papered over.

## Code layout

| File | Contents |
|---|---|
| `src/internal/plugin/registry.go` | Index model, fetch, ETag cache, resolution |
| `src/internal/plugin/registry_signature.go` | Minisign verification (phase 3) |
| `src/internal/plugin/install.go` | Staging, download, unpack, pin injection, atomic commit |
| `src/internal/cli/plugins.go` | `search`, `info`, `install`, `upgrade`, `uninstall` on the existing command group |
| `src/internal/config/validate.go` | `plugin_registries` validation |
| `registry/` | Entries, schema, and the CI workflow that gates them |

Reuse is deliberate: `fileSHA256`, `normalizeChecksum` and `verifyChecksum`
from `integrity.go`, `Load` / `Validate` from `manifest.go`, `Discover` from
`discovery.go`. The installer adds no second implementation of any check the
daemon already performs — a divergence between "what install accepts" and "what
the daemon accepts" would be the worst failure mode this change could have.

## Testing

- **Index**: schema round-trip, unknown `schema_version` fails closed, yanked
  releases skipped, resolution picks the right release, each rejection reports
  the predicate that eliminated the newest candidate.
- **Cache**: cold / warm / conditional-GET, `--offline` with and without a
  cache, corrupt cache re-fetched rather than fatal.
- **Install**: happy path end to end against a `file://` registry and a local
  archive; digest mismatch at both stages; archive traversal (`../`, absolute,
  symlink) rejected; manifest id ≠ requested id rejected; incompatible host
  version rejected before download (asserted by a fetch-counting transport);
  pin injected when absent, verified when present, install aborted on
  disagreement; interrupted install leaves nothing in `plugin_dirs`.
- **Upgrade**: swap, failed-validation rollback, `.bak` retention of exactly one
  generation.
- **Uninstall**: refusal while enabled, `--force`, idempotence.
- **CLI**: golden output for `search` / `info` / the trust summary, and a test
  that asserts `install` never writes to `apiary.yaml`.
