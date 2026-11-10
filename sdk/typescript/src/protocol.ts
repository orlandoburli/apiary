/**
 * Protocol envelope handling for Apiary plugins.
 *
 * Mirrors `sdk/plugin/protocol.go`: one request in on stdin, one response out
 * on stdout, then the process exits. Node builtins only.
 */

export const PROTOCOL_VERSION = 1;

// Capability names. `source` and `event_exporter` are live; the rest reserve
// the vocabulary for capabilities the host does not bridge yet.
export const CAPABILITY_SOURCE = "source";
export const CAPABILITY_RUNNER = "runner";
export const CAPABILITY_WORKFLOW_ACTION = "workflow_action";
export const CAPABILITY_APPROVAL_PROVIDER = "approval_provider";
export const CAPABILITY_SECRET_PROVIDER = "secret_provider";
export const CAPABILITY_EVENT_EXPORTER = "event_exporter";

const ENVELOPE_FIELDS = new Set([
  "protocol",
  "request_id",
  "capability",
  "method",
  "config",
  "payload",
]);

/**
 * The stream itself was unusable, so no response can be delivered.
 *
 * Thrown for a malformed, absent or over-long request. `main` reports it on
 * stderr and exits non-zero — deliberately distinct from `PluginError`, which
 * is a *delivered* answer.
 */
export class TransportError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "TransportError";
  }
}

/**
 * A failure the plugin reports to the host as a well-formed error response.
 *
 * `code` is machine-readable (`"read_failed"`, `"unsupported_method"`),
 * `message` is for humans. Both must be non-empty — the host logs them, and an
 * empty code is indistinguishable from success in an alert.
 */
export class PluginError extends Error {
  readonly code: string;

  constructor(code: string, message: string) {
    if (!code) {
      throw new Error("PluginError code must be non-empty");
    }
    if (!message) {
      throw new Error("PluginError message must be non-empty");
    }
    super(message);
    this.name = "PluginError";
    this.code = code;
  }

  toJSON(): ResponseError {
    return { code: this.code, message: this.message };
  }
}

/** One decoded request. `protocol` is always 1 by the time a handler runs. */
export class Request {
  protocol: number;
  request_id: string;
  capability: string;
  method: string;
  config: Record<string, unknown>;
  payload: unknown;

  constructor(init: Partial<Request> = {}) {
    this.protocol = init.protocol ?? 0;
    this.request_id = init.request_id ?? "";
    this.capability = init.capability ?? "";
    this.method = init.method ?? "";
    this.config = init.config ?? {};
    this.payload = init.payload ?? null;
  }

  static fromDict(raw: Record<string, unknown>): Request {
    const unknown = Object.keys(raw).filter((key) => !ENVELOPE_FIELDS.has(key));
    if (unknown.length > 0) {
      // The Go SDK decodes with DisallowUnknownFields; matching that here keeps
      // a typo in a hand-written request from being silently eaten.
      throw new TransportError(
        `unknown envelope field(s): ${unknown.sort().join(", ")}`,
      );
    }
    const config = raw["config"];
    return new Request({
      protocol: typeof raw["protocol"] === "number" ? raw["protocol"] : 0,
      request_id:
        typeof raw["request_id"] === "string" ? raw["request_id"] : "",
      capability:
        typeof raw["capability"] === "string" ? raw["capability"] : "",
      method: typeof raw["method"] === "string" ? raw["method"] : "",
      config:
        typeof config === "object" && config !== null && !Array.isArray(config)
          ? (config as Record<string, unknown>)
          : {},
      payload: raw["payload"] ?? null,
    });
  }
}

export interface ResponseError {
  code: string;
  message: string;
}

export interface Response {
  protocol: number;
  request_id: string;
  result?: unknown;
  error?: ResponseError;
}

/**
 * A handler returns the result value for a successful response, or throws
 * `PluginError` to return an error response. Anything JSON-serialisable works
 * as a result; the typed classes in `source.ts` encode themselves via
 * `toJSON`.
 */
export type Handler = (request: Request) => unknown | Promise<unknown>;

/** Options for `serveOne`; both streams are injectable for tests. */
export interface ServeOptions {
  /** The request text. Defaults to reading stdin to EOF. */
  input?: string;
  /** Called with the encoded response. Defaults to writing stdout. */
  output?: (response: string) => void;
}

/**
 * Encode a handler's return value, honouring `toJSON` on the typed mirrors.
 *
 * Returns `undefined` for `null`/`undefined` so the envelope can omit `result`
 * exactly as the Go structs' `omitempty` does.
 */
export function encodeResult(value: unknown): unknown {
  if (value === null || value === undefined) {
    return undefined;
  }
  if (
    typeof value === "object" &&
    "toJSON" in value &&
    typeof (value as { toJSON: unknown }).toJSON === "function"
  ) {
    return (value as { toJSON: () => unknown }).toJSON();
  }
  if (Array.isArray(value)) {
    return value.map((item) => encodeResult(item));
  }
  return value;
}

/**
 * Decode exactly one request object; trailing data is a transport error.
 *
 * `JSON.parse` rejects a second value glued onto the stream, which gives us the
 * same single-shot guarantee as the Go SDK's `decoder.More()` check.
 */
export function decodeRequest(raw: string): Request {
  const stripped = raw.trim();
  if (!stripped) {
    throw new TransportError("no request on stdin");
  }
  let value: unknown;
  try {
    value = JSON.parse(stripped);
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    if (/after JSON/i.test(message)) {
      throw new TransportError(
        "unexpected trailing data after the request; the protocol is single-shot",
      );
    }
    throw new TransportError(`decode request: ${message}`);
  }
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    const actual = Array.isArray(value) ? "array" : typeof value;
    throw new TransportError(`request must be a JSON object, got ${actual}`);
  }
  return Request.fromDict(value as Record<string, unknown>);
}

async function readStdin(): Promise<string> {
  const chunks: Buffer[] = [];
  for await (const chunk of process.stdin) {
    chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk as string));
  }
  return Buffer.concat(chunks).toString("utf8");
}

/**
 * Read one request, invoke `handler`, write one response.
 *
 * Single-shot on purpose: the host starts a fresh process per invocation, so
 * there is no loop, no state and no lifecycle to manage.
 */
export async function serveOne(
  handler: Handler,
  options: ServeOptions = {},
): Promise<void> {
  if (handler === null || handler === undefined) {
    throw new TransportError("plugin handler is required");
  }
  const input = options.input ?? (await readStdin());
  const write =
    options.output ??
    ((response: string) => {
      process.stdout.write(response);
    });

  const request = decodeRequest(input);
  const response: Response = {
    protocol: PROTOCOL_VERSION,
    request_id: request.request_id,
  };
  if (request.protocol !== PROTOCOL_VERSION) {
    response.error = {
      code: "unsupported_protocol",
      message: `protocol ${request.protocol} is unsupported; expected ${PROTOCOL_VERSION}`,
    };
  } else {
    try {
      const result = encodeResult(await handler(request));
      if (result !== undefined) {
        // Mirrors the Go SDK's `omitempty`: a nil result is left out of the
        // envelope rather than serialised as a null "answer".
        response.result = result;
      }
    } catch (err) {
      if (err instanceof PluginError) {
        response.error = err.toJSON();
      } else {
        throw err;
      }
    }
  }
  // Exactly one JSON object, nothing before or after it. Everything a plugin
  // wants to say to a human belongs on stderr.
  write(`${JSON.stringify(response)}\n`);
}

/**
 * Entry point for a plugin executable: serve one request and exit.
 *
 * Exits 0 once a response is on stdout — including an error response, which is
 * a delivered answer — and 2 when the transport failed and there is nothing to
 * deliver.
 */
export async function main(handler: Handler): Promise<void> {
  try {
    await serveOne(handler);
  } catch (err) {
    if (err instanceof TransportError) {
      process.stderr.write(`${err.message}\n`);
      process.exitCode = 2;
      return;
    }
    throw err;
  }
}
