# Tasks

## Phase 1 — Index format and read-only CLI

- [x] Define the registry entry format and `registry/schema/index-v1.json`.
- [x] Implement the index model, fetch, ETag cache and `--offline` (`registry_index.go`, `registry_source.go`).
- [x] Implement release resolution (yank, protocol, host semver, platform) with predicate-level error messages.
- [x] Add `plugin_registries` config plus validation (`https://` and `file://` only, empty list disables).
- [x] Add `apiary plugins search` and `apiary plugins info`.
- [x] Add the registry CI workflow: schema, digest re-computation, manifest cross-check, conformance kit run.
- [x] Compile and publish `index.json` from the docs deploy; seeded with `dev.apiary.routines` (the in-tree reference plugins publish no artifacts, so they cannot be listed).

Phase 1 also added `src/cmd/apiary-registry` (repository tooling: `check` verifies
every declared artifact end to end, `build` compiles the index) and the safe
archive extractor the checker needs, which Phase 2's installer reuses.

## Phase 2 — Install, upgrade, uninstall

- [x] Implement the staged install pipeline in `internal/plugin/install.go` (download, digest, safe unpack, validate, atomic commit).
- [x] Implement checksum pin injection and publisher-pin agreement checks.
- [x] Add `apiary plugins install` with `--dir`, `--yes`, `--sha256`, `--registry`, `--offline`.
- [x] Implement the trust summary output and confirmation prompt.
- [x] Add `apiary plugins upgrade` with one-generation `.bak` rollback and `--rollback`.
- [x] Add `apiary plugins uninstall` with the enabled-in-config refusal and `--force`.
- [x] Print the `plugins:` snippet and restart reminder; assert `apiary.yaml` is never written.

Phase 2 also added `Installed.VerifyPin()`, wired into `apiary plugins validate`
and `apiary validate`: discovery only checked that a pin was well-formed, so an
injected pin was verified per invocation by the daemon but never by the command
whose job is checking. Archive format detection now reads magic bytes rather
than the URL's filename.

## Phase 3 — Signing and mirrors

- [x] Sign `index.json` in CI with minisign; embed the public key in the binary.
- [x] Verify the signature before parsing, failing closed with no bypass flag.
- [x] Support per-registry `public_key` for internal mirrors.

Phase 3 verifies minisign signatures in-process (`crypto/ed25519` plus
BLAKE2b for minisign's prehashed mode), so no external tool is needed to check
one and `minisign -Vm` agrees with what the CLI does. Signing is wired but not
switched on: the docs deploy signs only when `APIARY_REGISTRY_MINISIGN_KEY`
exists, and released binaries verify only when `APIARY_REGISTRY_PUBLIC_KEY` was
stamped in at build time. Until both exist the index is reported as
`(unverified)` on every command — generating the signing identity is the
maintainer's call, not something this change invents. `registry/README.md`
documents the three one-time steps.

## Phase 4 — Documentation

- [x] Rewrite `docs/plugin-directory.md` around the registry, keeping the "pointer, not endorsement" framing.
- [x] Update `docs/plugins.md` installation/upgrade sections to lead with the CLI, keeping the manual path.
- [x] Add a "Publishing to the registry" section to `docs/plugin-sdk.md`.

Phase 4 also covered the pages the plan did not name but that had gone stale:
`docs/cli.md` gains an `apiary plugins` section, `docs/configuration.md` and
`schema/apiary.json` document `plugin_registries` (so the VS Code extension
validates it), and `docs/supported-integrations.md` points at
`plugins search --capability source`.

## Gates

- [x] Unit and integration tests per the testing section of `design.md`.
- [x] Run repository gates and GitNexus change detection.
- [x] Update `openspec/CHANGELOG.md` and archive the change.
