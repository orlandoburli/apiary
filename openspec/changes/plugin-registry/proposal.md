# Proposal: Plugin Registry and Command-Line Installs

## Why

Protocol 1 made third-party plugins possible; nothing made them *findable* or
*installable*. Today the whole distribution story is a markdown table
(`docs/plugin-directory.md`) plus a five-step manual procedure: build or
download an artifact, verify it out of band, `mkdir` a directory named after
the plugin id, copy two files in, `chmod +x`, run `apiary plugins validate`,
paste a `plugins:` block, restart. Every step is a place to get it subtly
wrong, and the two that matter most for safety — *which* artifact and *whether
it is the one the publisher shipped* — are the two Apiary offers no help with
at all.

The consequences are concrete:

- **Discovery is a doc page.** A new plugin reaches operators only if they
  re-read a table between releases. There is no `search`, no capability
  filter, no "is this compatible with my Apiary version" answer short of
  installing it and reading the validation error.
- **Compatibility is discovered after installation.** `Installed.Validate`
  checks the manifest's `apiary` semver constraint against the host — but only
  once the files are already on disk, already extracted, already executable.
- **The checksum pin is weak by construction, and we say so.** `docs/plugins.md`
  admits it: the digest lives beside the binary, so "anyone able to rewrite the
  executable can rewrite the pin too". It detects drift, not tampering.
- **Nothing is ever verified end to end.** The repository has a conformance kit
  and a CI workflow for it, and no published plugin is required to pass either.
  An operator has no signal beyond the publisher's own README.

The gap is that **Apiary has no name-to-artifact resolution layer**, so the
integrity, compatibility and provenance checks it already knows how to perform
all happen too late to prevent anything.

This change adds that layer — deliberately, and without becoming a package
manager. The current doctrine ("installation is placing files, deliberately";
plugins run unsandboxed as the daemon's user) is not softened. It is
mechanised, with the trust decision made *explicit and informed* instead of
implicit and undocumented.

---

## What Changes

| Area | Before | After |
|---|---|---|
| **Discovery** | A markdown table in the docs | A reviewed, CI-validated index compiled to static JSON, queryable with `apiary plugins search` |
| **Compatibility check** | After installation, from the on-disk manifest | Before download, from the index: host semver + protocol + os/arch |
| **Artifact integrity** | Operator verifies out of band, if they remember | Digest declared in the registry repository, verified by the CLI before anything is unpacked |
| **Checksum pin** | Optional, publisher-supplied, lives beside the binary | Injected at install time from the registry digest when the publisher did not pin one |
| **Install** | 5 manual steps, 2 of them security-relevant | `apiary plugins install <id>` — staged, verified, confirmed, atomically committed |
| **Upgrade / uninstall** | Prose in the docs | `apiary plugins upgrade` / `uninstall`, with one generation of rollback kept |
| **Conformance** | A kit nobody is required to run | Run in registry CI against the published artifact; the result is part of the entry |
| **Enablement** | Manual `plugins:` block | Still manual — `install` prints the snippet and never edits `apiary.yaml` |
| **Trust decision** | Undocumented, implicit in the copy commands | An explicit pre-install summary of the manifest's `security:` block, requiring confirmation |

---

## New Concepts

| Concept | Description |
|---|---|
| **Registry index** | A static JSON document (schema v1) listing plugins and their releases. Compiled by CI from PR-reviewed YAML entries under `registry/plugins/`, published beside the docs site. No server, no accounts, no uploads. |
| **Registry entry** | One YAML file per plugin id: identity, capabilities, links, license, and a list of releases. Adding or updating one is a pull request against this repository. |
| **Release artifact** | A per-`os`/`arch` archive URL hosted **by the publisher**, with the archive digest and the digest of the executable inside it. Apiary hosts metadata only; it never mirrors or re-hosts binaries. |
| **Staged install** | Download → verify digest → unpack → load and validate the manifest → present the trust summary → confirm → atomic `rename` into the plugin directory. Nothing is executed, and nothing lands in a searched directory, until every check has passed. |
| **Pin injection** | When the publisher's manifest carries no `checksum`, the installer writes `sha256:<executable_sha256>` from the index. The pin now originates in a repository the publisher does not control, which is what turns `integrityGuard` from drift detection into a supply-chain check. |
| **Registry mirror** | `plugin_registries:` accepts `file://` and internal HTTPS URLs, so air-gapped and enterprise installs resolve names against a copy they curate. An empty list disables the registry entirely. |

---

## Design

The mechanics — index schema, resolution rules, the install state machine,
failure and rollback semantics, and the signing plan — are in `design.md`. The
proposal-level commitments are:

1. **The index carries metadata, never code.** Artifacts stay on the
   publisher's release infrastructure. Apiary's registry is a directory with
   digests attached, and the docs' existing "a listing is a pointer to someone
   else's repository" wording is carried into the CLI output verbatim.
2. **Every check that can happen before download, happens before download.**
   Host-version constraint, protocol version, capability set, platform
   availability, and yanked-release status are all resolved from the index.
3. **Installation never enables anything.** The `plugins:` entry stays a
   deliberate edit by the operator, the daemon still has to be restarted, and
   `install` exits after printing the snippet. Installed ≠ running is preserved
   exactly as documented today.
4. **The trust summary is not optional.** Before the first byte is committed to
   a searched directory, the CLI prints the plugin id and version, the artifact
   URL, the verified digest, the manifest's `security:` declaration (network,
   read paths, write paths, `secret_env`) and the plain statement that the
   executable will run with the daemon's OS permissions. `--yes` skips the
   prompt, not the print.
5. **Registry CI is the gate, review is the trust.** Entries are validated,
   their digests re-computed from the real download, their embedded manifest
   cross-checked against the declared metadata, and the conformance kit run
   against the artifact. Listing remains *reviewed*, never *endorsed*.

---

## What Stays

- **Protocol 1 and the manifest format** — unchanged. A registry-installed
  plugin is byte-identical to a hand-installed one; the registry has no
  runtime presence at all.
- **Discovery, validation, and the invocation path** — `plugin.Discover`,
  `Installed.Validate`, `integrityGuard`, and the client are untouched. The
  installer *calls* them; it does not fork them.
- **`plugin_dirs` layering** — one directory per plugin id, duplicate ids across
  directories still an error. `install` writes into one searched directory and
  refuses to create a duplicate.
- **The daemon** — never contacts the registry, never downloads, never
  self-updates a plugin. Registry access lives entirely in the CLI.
- **`apiary plugins list | inspect | validate`** — unchanged semantics: what is
  on disk, not what is published.
- **The no-sandbox reality** — this change improves provenance, not isolation.
  Nothing here makes it safer to run a plugin you have not read.

---

## Implementation Plan

### Phase 1 — Index format and read-only CLI

1. Define the index schema (v1) and the per-plugin entry YAML under `registry/`.
2. Add `internal/plugin/registry.go`: index model, fetch, ETag-aware cache under
   `~/.cache/apiary/registry`, `--offline`.
3. Add `plugin_registries` config (default: the official index URL) plus
   config validation.
4. Add `apiary plugins search [query] [--capability]` and
   `apiary plugins info <id>[@version]`.
5. Add the registry CI workflow: schema validation, digest re-computation from
   the real artifact, manifest cross-check, conformance kit run.
6. Publish `index.json` from the existing docs deploy; seed it with
   `dev.apiary.routines` and the four reference plugins.

### Phase 2 — Install, upgrade, uninstall

7. Add `internal/plugin/install.go`: resolve → download → verify → unpack →
   validate → summarise → commit, with a staging directory under `TMPDIR` and a
   single atomic `rename`.
8. Implement pin injection and the manifest rewrite (only when unpinned).
9. Add `apiary plugins install <id>[@version]` with `--dir`, `--yes`,
   `--sha256` (pin-to-expected override), `--registry`, `--offline`.
10. Add `apiary plugins upgrade <id>` (keeps `<id>.bak` for one generation) and
    `apiary plugins uninstall <id>` (refuses while the id is enabled in
    `apiary.yaml`, unless `--force`).
11. Print the `plugins:` snippet and the restart reminder; never touch
    `apiary.yaml`.

### Phase 3 — Signing and mirrors

12. Sign `index.json` in CI (minisign); embed the public key in the binary.
13. Verify the signature before use; fail closed, with no bypass flag.
14. Document mirroring (`file://`, internal HTTPS) and the air-gapped workflow.

### Phase 4 — Documentation

15. Rewrite `docs/plugin-directory.md` around the registry; keep the
    "pointer, not endorsement" framing and the getting-listed instructions,
    retargeted at `registry/plugins/`.
16. Update the installation and upgrade sections of `docs/plugins.md` to lead
    with the CLI and keep the manual procedure as the offline path.
17. Add a "Publishing to the registry" section to `docs/plugin-sdk.md`.

---

## Out of Scope

- **Hosting artifacts.** Apiary stores metadata and digests; publishers host
  their own releases. No mirroring, no CDN, no upload endpoint.
- **Accounts, ownership, namespaces, or takedown flows.** A pull request is the
  publishing mechanism; repository access control is the ownership model.
- **Automatic or background upgrades.** No update checks in the daemon, no
  notifications, no `--auto-upgrade`. Upgrades are an operator action.
- **Dependency resolution between plugins.** Entries declare a host-version
  constraint, nothing more. Plugins do not depend on plugins.
- **Sandboxing.** The `security:` block stays an inspectable declaration, not an
  enforcement mechanism. Improving that is a separate change.
- **Enabling plugins from the CLI.** Writing `plugins:` entries into
  `apiary.yaml` programmatically is deliberately excluded from v1.
- **Telemetry.** No download counts, no install pings, no phone-home.
- **Registry access from the daemon.** The daemon's plugin path stays offline.

---

## Migration

Nothing existing breaks: the registry is an additional way to obtain files that
the rest of the system already knows how to handle.

| Existing state | Behavior after change |
|---|---|
| Hand-installed plugin directories | Unchanged — discovered, validated, and run exactly as before |
| Manifest with a publisher `checksum` | Unchanged; the installer never overwrites an existing pin |
| Manifest without a `checksum` | Unchanged when hand-installed; pinned from the index when installed via the CLI |
| No `plugin_registries` in config | The official index is used for `search`/`info`/`install`; the daemon is unaffected |
| `plugin_registries: []` | Registry subcommands report that the registry is disabled and exit non-zero; manual installation still works |
| Air-gapped install | `--offline` against a warm cache, or a `file://` mirror |

Operators who prefer the manual procedure keep it: it remains documented, it
remains supported, and it remains the only path that needs no network at all.
