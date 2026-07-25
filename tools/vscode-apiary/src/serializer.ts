import * as yaml from 'js-yaml';
import { ApiaryConfig } from './parser';

function stripUndefined(value: unknown): unknown {
  if (value === null || value === undefined) {
    return undefined;
  }
  if (Array.isArray(value)) {
    const cleaned = value.map(stripUndefined).filter(v => v !== undefined);
    return cleaned.length > 0 ? cleaned : undefined;
  }
  if (typeof value === 'object') {
    const result: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
      const cleaned = stripUndefined(v);
      if (cleaned !== undefined) {
        result[k] = cleaned;
      }
    }
    return Object.keys(result).length > 0 ? result : undefined;
  }
  return value;
}

/**
 * Serialize an ApiaryConfig back to YAML text.
 *
 * Note: YAML comments are not preserved because js-yaml does not track them.
 * Semantic content (all non-comment fields) is faithfully round-tripped.
 */
export function serializeApiary(config: ApiaryConfig): string {
  const cleaned = stripUndefined(config) as ApiaryConfig;
  return yaml.dump(cleaned, {
    indent: 2,
    lineWidth: -1,
    noRefs: true,
    sortKeys: false,
  });
}
