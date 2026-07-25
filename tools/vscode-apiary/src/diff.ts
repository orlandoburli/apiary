export interface DiffResult {
  hasChanges: boolean;
  unifiedDiff: string;
}

interface EditOp {
  op: 'keep' | 'insert' | 'delete';
  aIdx: number;
  bIdx: number;
}

// Myers diff — shortest edit script between two line arrays.
function shortestEditScript(a: string[], b: string[]): EditOp[] {
  const n = a.length;
  const m = b.length;
  const max = n + m;

  if (max === 0) { return []; }

  const v: number[] = new Array(2 * max + 1).fill(0);
  const trace: number[][] = [];

  for (let d = 0; d <= max; d++) {
    trace.push(v.slice());
    for (let k = -d; k <= d; k += 2) {
      const ki = k + max;
      let x: number;
      if (k === -d || (k !== d && v[ki - 1] < v[ki + 1])) {
        x = v[ki + 1];
      } else {
        x = v[ki - 1] + 1;
      }
      let y = x - k;
      while (x < n && y < m && a[x] === b[y]) { x++; y++; }
      v[ki] = x;
      if (x >= n && y >= m) {
        return backtrack(trace, a, b, max, d);
      }
    }
  }
  return backtrack(trace, a, b, max, max);
}

function backtrack(
  trace: number[][],
  a: string[],
  b: string[],
  max: number,
  d: number,
): EditOp[] {
  const ops: EditOp[] = [];
  let x = a.length;
  let y = b.length;

  for (let dd = d; dd > 0; dd--) {
    const vv = trace[dd];
    const k = x - y;
    const ki = k + max;
    let prevK: number;
    if (k === -dd || (k !== dd && vv[ki - 1] < vv[ki + 1])) {
      prevK = k + 1;
    } else {
      prevK = k - 1;
    }
    const prevX = vv[prevK + max];
    const prevY = prevX - prevK;

    while (x > prevX && y > prevY) {
      ops.unshift({ op: 'keep', aIdx: x - 1, bIdx: y - 1 });
      x--; y--;
    }
    if (x === prevX) {
      ops.unshift({ op: 'insert', aIdx: prevX, bIdx: y - 1 });
      y--;
    } else {
      ops.unshift({ op: 'delete', aIdx: x - 1, bIdx: prevY });
      x--;
    }
  }

  while (x > 0 && y > 0) {
    ops.unshift({ op: 'keep', aIdx: x - 1, bIdx: y - 1 });
    x--; y--;
  }

  return ops;
}

interface MappedLine {
  op: 'keep' | 'insert' | 'delete';
  aLine: number; // 1-indexed in old file (0 = N/A)
  bLine: number; // 1-indexed in new file (0 = N/A)
  text: string;
}

/**
 * Compute a unified diff between two YAML texts.
 *
 * Returns the unified diff string and a hasChanges flag.
 */
export function computeDiff(oldText: string, newText: string): DiffResult {
  if (oldText === newText) {
    return { hasChanges: false, unifiedDiff: 'No changes.' };
  }

  const oldLines = oldText.split('\n');
  const newLines = newText.split('\n');
  const ops = shortestEditScript(oldLines, newLines);

  // Build a flat list of mapped lines
  const mapped: MappedLine[] = [];
  let aLine = 1;
  let bLine = 1;

  for (const op of ops) {
    if (op.op === 'keep') {
      mapped.push({ op: 'keep', aLine, bLine, text: oldLines[op.aIdx] });
      aLine++; bLine++;
    } else if (op.op === 'delete') {
      mapped.push({ op: 'delete', aLine, bLine: 0, text: oldLines[op.aIdx] });
      aLine++;
    } else {
      mapped.push({ op: 'insert', aLine: 0, bLine, text: newLines[op.bIdx] });
      bLine++;
    }
  }

  const changeIndices = mapped
    .map((l, i) => (l.op !== 'keep' ? i : -1))
    .filter(i => i !== -1);

  if (changeIndices.length === 0) {
    return { hasChanges: false, unifiedDiff: 'No changes.' };
  }

  const CONTEXT = 3;
  const unifiedLines: string[] = [
    '--- apiary.yaml\t(original)',
    '+++ apiary.yaml\t(modified)',
  ];

  let ci = 0;
  while (ci < changeIndices.length) {
    const start = Math.max(0, changeIndices[ci] - CONTEXT);
    let end = Math.min(mapped.length - 1, changeIndices[ci] + CONTEXT);

    while (ci + 1 < changeIndices.length && changeIndices[ci + 1] <= end + CONTEXT) {
      ci++;
      end = Math.min(mapped.length - 1, changeIndices[ci] + CONTEXT);
    }

    const hunk = mapped.slice(start, end + 1);
    const oldStart = hunk.find(l => l.aLine > 0)?.aLine ?? 1;
    const newStart = hunk.find(l => l.bLine > 0)?.bLine ?? 1;
    const oldCount = hunk.filter(l => l.op !== 'insert').length;
    const newCount = hunk.filter(l => l.op !== 'delete').length;

    unifiedLines.push(`@@ -${oldStart},${oldCount} +${newStart},${newCount} @@`);
    for (const l of hunk) {
      const prefix = l.op === 'keep' ? ' ' : l.op === 'delete' ? '-' : '+';
      unifiedLines.push(`${prefix}${l.text}`);
    }

    ci++;
  }

  return { hasChanges: true, unifiedDiff: unifiedLines.join('\n') };
}
