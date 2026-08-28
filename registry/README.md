# Apiary plugin registry

This directory is the registry: one reviewed YAML file per plugin, compiled by
CI into the JSON index that `apiary plugins search` and `apiary plugins info`
read.

It holds **metadata only**. Artifacts stay on their publisher's release
infrastructure — Apiary never mirrors, re-hosts, or serves a binary, and the
daemon never contacts a registry at all. A listing is a pointer to someone
else's repository. It is reviewed; it is not endorsed.

```
registry/
├── plugins/<plugin-id>.yaml   ← add yours here, via pull request
└── schema/index-v1.json       ← the schema of the compiled index
```

## Getting listed

Open a pull request adding `plugins/<your-plugin-id>.yaml`. The filename must be
the plugin id. Requirements:

- The repository must be **publicly readable** — an operator has to be able to
  read the code before running it as their daemon's user.
- Every artifact needs both digests: `archive_sha256` (the file you publish) and
  `executable_sha256` (the executable inside it, after unpacking). There is no
  unpinned release.
- Releases are **immutable**. A bad release is marked `yanked: true` with a
  `yanked_reason`; it is never rewritten or deleted, because the index has to
  stay honest about what was once resolvable.

```yaml
schema_version: 1
id: com.example.nagios
summary: One sentence on what it does.
capabilities: [source]
repository: https://github.com/example/apiary-nagios
license: MIT

# Optional: the config CI runs the protocol conformance kit with. Without it the
# release is published as "conformance not run" — an honest absence rather than
# an unearned pass.
conformance_config:
  api_url: https://example.invalid/api

releases:
  - version: 1.0.0
    apiary: ">= 0.13.0-0"
    protocol: 1
    artifacts:
      - os: linux
        arch: amd64
        url: https://github.com/example/apiary-nagios/releases/download/v1.0.0/nagios_1.0.0_linux_amd64.tar.gz
        archive_sha256: "…"
        executable_sha256: "…"
```

## What CI checks

Nothing in a listing is taken on trust. For every artifact, on every pull
request:

1. The entry is validated — id, semver, capability vocabulary, protocol, digests.
2. The artifact is downloaded and its `archive_sha256` re-derived.
3. It is unpacked, and the `apiary-plugin.json` inside is cross-checked against
   the entry: id, version, protocol, `apiary` constraint, capabilities.
4. `executable_sha256` is re-derived from the executable the manifest names.
5. If the entry declares `conformance_config`, the protocol conformance kit runs
   against the published executable, and its verdict is recorded in the index.

A conformance failure does **not** block a listing — the verdict is published
instead, and `apiary plugins info` shows it. The registry describes plugins; it
does not certify them.

```bash
make registry-check
```

## Signing

The published index is signed with [minisign](https://jedisct1.github.io/minisign/),
so a client can tell that the digests it is about to trust are the ones this
repository reviewed. Signature verification covers the index, which carries the
digests, which cover the artifacts. It does **not** authenticate plugin
publishers — signing the artifacts themselves is a separate problem.

Signing is not yet switched on. Turning it on is three steps, all one-time:

1. **Generate a passwordless key** (CI cannot answer a password prompt):

   ```bash
   minisign -G -W -p apiary-registry.pub -s apiary-registry.key
   ```

2. **Store the secret key** as the repository secret
   `APIARY_REGISTRY_MINISIGN_KEY` (the whole file, comment line included). The
   docs deploy signs `index.json` with it and publishes `index.json.minisig`
   beside it; while the secret is unset, the index publishes unsigned.

3. **Stamp the public key into released binaries.** Set
   `APIARY_REGISTRY_PUBLIC_KEY` to the base64 line of `apiary-registry.pub` in
   the release environment — the Makefile and GoReleaser both pass it to
   `-X …/internal/plugin.OfficialRegistryPublicKey`.

Until step 3 lands in a released binary, `apiary plugins …` reports the official
index as `(unverified)` on every command. That is deliberate: an unverified
index is a real state, and saying nothing would let it read as verified.

Once a key is pinned there is **no way to skip verification** — no flag, no
environment variable. A missing, malformed, or mismatched signature fails the
command.

Anyone can check the published index by hand:

```bash
curl -sO https://orlandoburli.com.br/apiary/registry/v1/index.json
curl -sO https://orlandoburli.com.br/apiary/registry/v1/index.json.minisig
minisign -Vm index.json -P "<the base64 public key line>"
```

## Mirroring

An internal or air-gapped install points `plugin_registries` at its own copy.
Both forms are accepted — a bare URL, or a mapping that pins the mirror to its
own signing key:

```yaml
plugin_registries:
  # The official index, verified against the key built into the binary.
  - https://orlandoburli.com.br/apiary/registry/v1/index.json
  # An internal mirror, verified against a key this organisation controls.
  - url: file:///opt/apiary/registry/index.json
    public_key: RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3
```

Registries are consulted in order and the first hit wins, so a mirror listed
first deliberately shadows the official index. Only `https://` and `file://` are
accepted: digests protect the payload, but nothing protects a plaintext
resolution.

A mirror can either carry the upstream signature verbatim — copy
`index.json.minisig` next to `index.json` and pin the upstream public key — or
re-sign the index with its own key and pin that. Both verify identically.

Air-gapped installs have two options: a `file://` mirror as above, or
`--offline`, which uses the cached index and never touches the network. The
cache is verified on the way out as well as on the way in, so a cache poisoned
on disk is caught rather than served.

## Building the index locally

```bash
make registry-build
```

Writes `docs/registry/v1/index.json`, which the docs deploy publishes at
`https://orlandoburli.com.br/apiary/registry/v1/index.json`. To point a CLI at a
local build:

```bash
apiary plugins search --registry file://$PWD/docs/registry/v1/index.json
```
