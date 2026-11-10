/**
 * Apiary plugin SDK for TypeScript/Node.
 *
 * Wraps Apiary's single-shot JSON plugin protocol (version 1) so a plugin is
 * one handler function:
 *
 * ```ts
 * import { PluginError, Request, main } from "@apiary/plugin";
 * import { SOURCE_METHOD_POLL, SourceItem, SourcePollResult } from "@apiary/plugin";
 *
 * function handle(request: Request) {
 *   if (request.method === SOURCE_METHOD_POLL) {
 *     return new SourcePollResult([new SourceItem({ id: "1", title: "Hello" })]);
 *   }
 *   throw new PluginError("unsupported_method", `unknown method ${request.method}`);
 * }
 *
 * await main(handle);
 * ```
 *
 * The version tracks the **protocol**, not the Apiary release: 1.x speaks
 * protocol 1.
 */

export * from "./protocol.js";
export * from "./source.js";

export const VERSION = "1.0.0";
