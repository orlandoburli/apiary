/**
 * Typed mirrors of the `source` capability wire contract.
 *
 * A line-for-line counterpart of `sdk/plugin/source.go`. Optional fields are
 * omitted from the encoded JSON, matching the Go structs' `omitempty` tags, so
 * a plugin written against either SDK puts the same bytes on the wire.
 */

/** Method names a source plugin switches on. */
export const SOURCE_METHOD_POLL = "poll";
export const SOURCE_METHOD_ACKNOWLEDGE = "acknowledge";
export const SOURCE_METHOD_WRITE_RESULT = "write_result";

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** Drop empty optional fields — the `omitempty` of the Go structs. */
function compact(values: Record<string, unknown>): Record<string, unknown> {
  const encoded: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(values)) {
    if (value === undefined || value === null || value === "") {
      continue;
    }
    if (Array.isArray(value) && value.length === 0) {
      continue;
    }
    if (isPlainObject(value) && Object.keys(value).length === 0) {
      continue;
    }
    encoded[key] = value;
  }
  return encoded;
}

export interface SourceItemInit {
  /** Source-native identifier and the host's dedup key. Required. */
  id: string;
  number?: string;
  title?: string;
  description?: string;
  labels?: string[];
  type?: string;
  priority?: string;
  state?: string;
  url?: string;
  metadata?: Record<string, unknown>;
  created_at?: string;
  updated_at?: string;
}

/**
 * One unit of work surfaced by a source plugin.
 *
 * `id` is the source-native identifier **and the host's dedup key**: it must be
 * stable for the lifetime of the item and unique per dispatch-worthy
 * occurrence. Items without one are dropped by the host, so it is the only
 * required field. Returning the same item on every poll while it stays
 * relevant is the expected shape.
 *
 * `labels` drive workflow trigger matching and use `key:value` form.
 * `created_at` / `updated_at` are RFC3339 strings; empty means "now".
 */
export class SourceItem {
  id: string;
  number: string;
  title: string;
  description: string;
  labels: string[];
  type: string;
  priority: string;
  state: string;
  url: string;
  metadata: Record<string, unknown>;
  created_at: string;
  updated_at: string;

  constructor(init: SourceItemInit) {
    this.id = init.id;
    this.number = init.number ?? "";
    this.title = init.title ?? "";
    this.description = init.description ?? "";
    this.labels = init.labels ? [...init.labels] : [];
    this.type = init.type ?? "";
    this.priority = init.priority ?? "";
    this.state = init.state ?? "";
    this.url = init.url ?? "";
    this.metadata = init.metadata ? { ...init.metadata } : {};
    this.created_at = init.created_at ?? "";
    this.updated_at = init.updated_at ?? "";
  }

  static fromDict(raw: Record<string, unknown>): SourceItem {
    const read = (key: string): string =>
      typeof raw[key] === "string" ? (raw[key] as string) : "";
    const labels = raw["labels"];
    const metadata = raw["metadata"];
    return new SourceItem({
      id: read("id"),
      number: read("number"),
      title: read("title"),
      description: read("description"),
      labels: Array.isArray(labels) ? [...(labels as string[])] : [],
      type: read("type"),
      priority: read("priority"),
      state: read("state"),
      url: read("url"),
      metadata: isPlainObject(metadata) ? { ...metadata } : {},
      created_at: read("created_at"),
      updated_at: read("updated_at"),
    });
  }

  toJSON(): Record<string, unknown> {
    const encoded = compact({
      number: this.number,
      title: this.title,
      description: this.description,
      labels: [...this.labels],
      type: this.type,
      priority: this.priority,
      state: this.state,
      url: this.url,
      metadata: { ...this.metadata },
      created_at: this.created_at,
      updated_at: this.updated_at,
    });
    // id is never omitted: an item without one is not dispatchable.
    return { id: this.id, ...encoded };
  }
}

/**
 * Payload of `poll`.
 *
 * `since` is the previous poll time (RFC3339, empty on the first poll) and is
 * informational: plugins that re-return ongoing items may ignore it.
 * `states` / `labels` are the source's `filters:` from apiary.yaml, forwarded
 * so the plugin can filter at the backend where possible.
 */
export class SourcePollRequest {
  since: string;
  states: string[];
  labels: string[];

  constructor(init: Partial<SourcePollRequest> = {}) {
    this.since = init.since ?? "";
    this.states = init.states ? [...init.states] : [];
    this.labels = init.labels ? [...init.labels] : [];
  }

  static fromDict(raw?: Record<string, unknown> | null): SourcePollRequest {
    const value = raw ?? {};
    return new SourcePollRequest({
      since: typeof value["since"] === "string" ? value["since"] : "",
      states: Array.isArray(value["states"])
        ? [...(value["states"] as string[])]
        : [],
      labels: Array.isArray(value["labels"])
        ? [...(value["labels"] as string[])]
        : [],
    });
  }
}

/** Result of `poll`: the plugin's current work items. */
export class SourcePollResult {
  items: SourceItem[];

  constructor(items: SourceItem[] = []) {
    this.items = items;
  }

  toJSON(): Record<string, unknown> {
    // `items` is always present, empty array included — "no work right now" is
    // an answer, and dropping the key would look like a malformed result.
    return { items: this.items.map((item) => item.toJSON()) };
  }
}

/** Payload of `acknowledge`: the item and the host's ack action. */
export class SourceAckRequest {
  item: SourceItem;
  action: string;

  constructor(init: { item: SourceItem; action?: string }) {
    this.item = init.item;
    this.action = init.action ?? "";
  }

  static fromDict(raw?: Record<string, unknown> | null): SourceAckRequest {
    const value = raw ?? {};
    const item = value["item"];
    return new SourceAckRequest({
      item: SourceItem.fromDict(isPlainObject(item) ? item : {}),
      action: typeof value["action"] === "string" ? value["action"] : "",
    });
  }
}

/** Payload of `write_result`: the run outcome for an item. */
export class SourceWriteResultRequest {
  item: SourceItem;
  success: boolean;
  output: string;
  error: string;

  constructor(init: {
    item: SourceItem;
    success?: boolean;
    output?: string;
    error?: string;
  }) {
    this.item = init.item;
    this.success = init.success ?? false;
    this.output = init.output ?? "";
    this.error = init.error ?? "";
  }

  static fromDict(
    raw?: Record<string, unknown> | null,
  ): SourceWriteResultRequest {
    const value = raw ?? {};
    const item = value["item"];
    return new SourceWriteResultRequest({
      item: SourceItem.fromDict(isPlainObject(item) ? item : {}),
      success: value["success"] === true,
      output: typeof value["output"] === "string" ? value["output"] : "",
      error: typeof value["error"] === "string" ? value["error"] : "",
    });
  }
}

/** The conventional `{"ok": true}` answer to a write-back method. */
export class SourceOKResult {
  ok: boolean;

  constructor(ok = true) {
    this.ok = ok;
  }

  toJSON(): Record<string, unknown> {
    return { ok: this.ok };
  }
}
