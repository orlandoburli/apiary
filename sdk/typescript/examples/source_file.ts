#!/usr/bin/env node
/**
 * Reference protocol-1 source plugin in TypeScript/Node.
 *
 * Behaviourally identical to the Go `src/examples/plugins/source-file` plugin:
 * it polls Apiary work items from the JSON file named by `config.path`, so any
 * external process can drop items there and have Apiary dispatch workflows for
 * them. It is also the plugin the conformance kit runs to validate this SDK.
 */

import { readFileSync } from "node:fs";

import {
  CAPABILITY_SOURCE,
  PluginError,
  Request,
  main,
} from "../src/index.js";
import {
  SOURCE_METHOD_ACKNOWLEDGE,
  SOURCE_METHOD_POLL,
  SOURCE_METHOD_WRITE_RESULT,
  SourceItem,
  SourceOKResult,
  SourcePollResult,
} from "../src/index.js";

function poll(request: Request): SourcePollResult {
  const path = request.config["path"];
  if (typeof path !== "string" || path === "") {
    throw new PluginError("invalid_config", "config.path is required");
  }
  let raw: string;
  try {
    raw = readFileSync(path, "utf8");
  } catch (err) {
    if ((err as NodeJS.ErrnoException).code === "ENOENT") {
      return new SourcePollResult([]); // an absent file simply means no work yet
    }
    throw new PluginError("read_failed", String(err));
  }
  let items: unknown;
  try {
    items = JSON.parse(raw);
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    throw new PluginError(
      "invalid_items",
      `${path} must hold a JSON array of items: ${message}`,
    );
  }
  if (!Array.isArray(items)) {
    throw new PluginError(
      "invalid_items",
      `${path} must hold a JSON array of items`,
    );
  }
  process.stderr.write(`poll: ${items.length} item(s) from ${path}\n`); // diagnostics → stderr
  return new SourcePollResult(items.map((item) => SourceItem.fromDict(item)));
}

function handle(request: Request): unknown {
  if (request.capability !== CAPABILITY_SOURCE) {
    throw new PluginError("unsupported_capability", "expected capability source");
  }
  switch (request.method) {
    case SOURCE_METHOD_POLL:
      return poll(request);
    case SOURCE_METHOD_ACKNOWLEDGE:
    case SOURCE_METHOD_WRITE_RESULT:
      // Nothing to mark in a plain file; report success so host logs stay clean.
      return new SourceOKResult();
    default:
      throw new PluginError(
        "unsupported_method",
        `unknown method ${request.method}`,
      );
  }
}

await main(handle);
