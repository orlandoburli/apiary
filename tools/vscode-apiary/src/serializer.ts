import * as yaml from 'js-yaml';
import { ApiaryConfig } from './parser';

// Convert the ApiaryConfig model back to a YAML string.
// Round-trip is semantic: comments and formatting are not preserved, but all
// structured data is. The caller should warn users that manual formatting will
// be lost if they choose to save.
export function serializeApiary(config: ApiaryConfig): string {
  // Use js-yaml dump with sensible defaults for readability.
  return yaml.dump(stripUndefined(config), {
    indent: 2,
    lineWidth: 120,
    noRefs: true,
    forceQuotes: false,
    quotingType: '"',
    skipInvalid: true,
  });
}

// Remove undefined values and empty objects/arrays that would clutter the YAML.
function stripUndefined(value: unknown): unknown {
  if (value === null || value === undefined) { return undefined; }
  if (Array.isArray(value)) {
    const filtered = value.map(stripUndefined).filter(v => v !== undefined);
    return filtered.length === 0 ? undefined : filtered;
  }
  if (typeof value === 'object') {
    const result: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
      const stripped = stripUndefined(v);
      if (stripped !== undefined) {
        result[k] = stripped;
      }
    }
    return Object.keys(result).length === 0 ? undefined : result;
  }
  return value;
}
