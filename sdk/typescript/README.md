# @apiary/plugin (TypeScript/Node)

The official TypeScript/Node SDK for [Apiary](https://github.com/orlandoburli/apiary)
plugins. It handles the protocol envelope — the single-shot JSON exchange on
stdin/stdout — and mirrors the typed `source` structs, so a plugin is one
handler function.

Node builtins only, on purpose: a plugin is a short-lived subprocess the daemon
spawns, and it should not drag a dependency tree into that path. The package
has **no runtime dependencies** (the only devDependencies are `typescript` and
`@types/node`, used to build it).

## Install

```bash
cd sdk/typescript
npm install
npm run build
npm install /path/to/sdk/typescript   # or: npm link
```

The package is **not published to npm**; install it from the repository (or
build it and vendor `dist/`).

## Use

```ts
#!/usr/bin/env node
import {
  CAPABILITY_SOURCE,
  PluginError,
  Request,
  main,
} from "@apiary/plugin";
import {
  SOURCE_METHOD_ACKNOWLEDGE,
  SOURCE_METHOD_POLL,
  SOURCE_METHOD_WRITE_RESULT,
  SourceItem,
  SourceOKResult,
  SourcePollResult,
} from "@apiary/plugin";

function handle(request: Request): unknown {
  if (request.capability !== CAPABILITY_SOURCE) {
    throw new PluginError("unsupported_capability", "expected capability source");
  }
  if (request.method === SOURCE_METHOD_POLL) {
    return new SourcePollResult([
      new SourceItem({
        id: "ts-1",
        title: "Hello from TypeScript",
        labels: ["origin:typescript"],
        state: "open",
      }),
    ]);
  }
  if (
    request.method === SOURCE_METHOD_ACKNOWLEDGE ||
    request.method === SOURCE_METHOD_WRITE_RESULT
  ) {
    return new SourceOKResult();
  }
  throw new PluginError("unsupported_method", `unknown method ${request.method}`);
}

await main(handle);
```

Compile it with `tsc`, make the emitted script executable (or invoke it with
`node`), pair it with a manifest declaring `"capabilities": ["source"]`, and
install it like any other plugin.

| Symbol | Purpose |
|---|---|
| `main(handler)` | Serve one request on stdin/stdout and exit — all a plugin's entry point needs |
| `serveOne(handler, { input, output })` | The engine behind `main`, with injectable streams — use it in tests |
| `Request` | The decoded envelope: `protocol`, `request_id`, `capability`, `method`, `config`, `payload` |
| `PluginError(code, message)` | Throw it to return an error response; both fields must be non-empty |
| `TransportError` | The stream itself was unusable — no response can be delivered, `main` exits 2 |
| `@apiary/plugin` | Typed mirrors of `sdk/plugin/source.go`: `SourceItem`, `SourcePollRequest`/`SourcePollResult`, `SourceAckRequest`, `SourceWriteResultRequest`, `SourceOKResult` |

Your handler returns the result value; anything with a `toJSON()` (every typed
mirror here) encodes itself. Diagnostics go to **stderr** — stdout carries the
one response object and nothing else.

## Versioning

The version tracks the **protocol**, not the Apiary release: `@apiary/plugin`
1.x speaks protocol 1, and a future protocol bump gets a new major version.
See [Versioning and the protocol](../../docs/plugin-sdk.md#versioning-and-the-protocol).

## Tests and conformance

```bash
cd sdk/typescript && npm install && npm test   # unit tests (node:test)
make conformance                               # the shared protocol corpus
```

The [conformance kit](../conformance/README.md) runs this SDK's example plugin
(`examples/source_file.ts`, built to `dist/examples/source_file.js`) against
the same golden corpus as every other SDK.
