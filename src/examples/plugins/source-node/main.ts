// Reference protocol-1 source plugin in TypeScript (Node): polls Apiary work
// items from the JSON file named by config.path — the same behavior as the Go
// source-file plugin. No runtime dependencies; compiled with tsc to a single
// executable script (see package.json).
import { readFileSync } from "node:fs";

interface Request {
  protocol: number;
  request_id: string;
  capability: string;
  method: string;
  config?: Record<string, unknown>;
  payload?: unknown;
}

interface ResponseError {
  code: string;
  message: string;
}

function respond(requestId: string, body: { result?: unknown; error?: ResponseError }): void {
  // Exactly one JSON object on stdout — all diagnostics belong on stderr.
  process.stdout.write(JSON.stringify({ protocol: 1, request_id: requestId, ...body }) + "\n");
}

function poll(config: Record<string, unknown>): { result?: unknown; error?: ResponseError } {
  const path = typeof config.path === "string" ? config.path : "";
  if (path === "") {
    return { error: { code: "invalid_config", message: "config.path is required" } };
  }
  let raw: string;
  try {
    raw = readFileSync(path, "utf8");
  } catch (err) {
    if ((err as NodeJS.ErrnoException).code === "ENOENT") {
      return { result: { items: [] } }; // an absent file simply means no work yet
    }
    return { error: { code: "read_failed", message: String(err) } };
  }
  let items: unknown;
  try {
    items = JSON.parse(raw);
  } catch (err) {
    return { error: { code: "invalid_items", message: `${path} must hold a JSON array of items: ${err}` } };
  }
  if (!Array.isArray(items)) {
    return { error: { code: "invalid_items", message: `${path} must hold a JSON array of items` } };
  }
  console.error(`poll: ${items.length} item(s) from ${path}`); // diagnostics → stderr
  return { result: { items } };
}

const input = readFileSync(0, "utf8"); // the single request, stdin → EOF
const req = JSON.parse(input) as Request;

if (req.protocol !== 1) {
  respond(req.request_id ?? "", { error: { code: "unsupported_protocol", message: "expected protocol 1" } });
} else if (req.capability !== "source") {
  respond(req.request_id, { error: { code: "unsupported_capability", message: "expected capability source" } });
} else if (req.method === "poll") {
  respond(req.request_id, poll(req.config ?? {}));
} else if (req.method === "acknowledge" || req.method === "write_result") {
  respond(req.request_id, { result: { ok: true } }); // nothing to mark in a plain file
} else {
  respond(req.request_id, { error: { code: "unsupported_method", message: `unknown method ${req.method}` } });
}
