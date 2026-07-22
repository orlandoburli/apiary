# Design: versioned plugin SDK

## Manifest and discovery

Each installed plugin occupies one directory containing `apiary-plugin.json` and
its executable. The manifest declares a reverse-DNS ID, semantic version, Apiary
version constraint, protocol version, executable, capabilities, configuration
JSON Schema, and security requirements. Apiary searches configured plugin
directories deterministically and rejects duplicate IDs.

The top-level `plugins` configuration enables installed plugins by ID and supplies
instance configuration. Disabled entries remain inspectable but are not started
or schema-validated.

## Protocol

Protocol `1` is newline-delimited JSON over stdin/stdout. Apiary starts a fresh
child process per invocation, sends one envelope containing protocol, request ID,
capability, method, configuration, and payload, then reads one response envelope.
The child must return the same request ID and either a result or structured error.

Per-invocation processes deliberately trade startup cost for strong crash and
state isolation. Context cancellation and configured deadlines terminate only the
plugin process. Stderr is captured separately and truncated in reported errors;
stdout is bounded before decoding.

## Compatibility and validation

- Manifests use semantic versions and an Apiary compatibility constraint.
- Unknown protocol versions, capabilities, malformed schemas, unsafe executable
  paths, duplicate IDs, and incompatible Apiary versions fail with remediation.
- A small built-in JSON Schema validator supports the manifest subset used by
  plugin configuration: type, properties, required, additionalProperties, enum,
  items, and scalar bounds. Unsupported schema keywords fail closed.

## Security

Discovery never executes a plugin. Enabling is explicit. Executables must resolve
inside the plugin directory and may not be symlinks. Only explicitly named secret
environment variables are forwarded, and their values never appear in protocol
payloads or diagnostics. Installation and upgrade remain operator-controlled file
operations with checksum/signature verification documented as a trust boundary.
