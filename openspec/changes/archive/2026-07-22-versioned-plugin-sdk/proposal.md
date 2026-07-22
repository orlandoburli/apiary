# Versioned plugin SDK and extension manifest

## Motivation

Apiary's source and runner extension contracts are currently Go interfaces backed
by statically registered factories. Adding every integration to the core binary
would couple unrelated release cycles and let third-party failures affect the
dispatcher process.

## Scope

- Define a versioned plugin manifest for sources, runners, workflow actions,
  approval providers, secret providers, and event exporters.
- Discover installed plugins from explicit directories, validate compatibility
  and capabilities, and expose inspection through the CLI.
- Validate enabled plugin configuration against each manifest's JSON Schema as
  part of `apiary validate`.
- Define a newline-delimited JSON request/response protocol over child-process
  stdio with deadlines, bounded output, and crash isolation.
- Include a reference event-exporter plugin and document installation, trust,
  upgrades, permissions, and secret handling.

Remote registries, automatic binary downloads, sandbox/container provisioning,
and migration of every built-in integration are deferred. Built-ins continue to
use the same internal contracts that plugin proxies adapt to.
