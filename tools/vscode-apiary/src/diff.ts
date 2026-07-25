// Minimal unified-diff implementation for YAML preview.
// Returns an array of DiffLine objects that the webview can render.

export interface DiffLine {
  type: 'context' | 'added' | 'removed';
  lineNo: number;   // 1-based in the "new" file for added/context; "old" for removed
  text: string;
}

// Compute a line-by-line diff between `before` and `after`.
// Returns a flat list of DiffLine objects with a small amount of context around changes.
export function computeDiff(before: string, after: string, contextLines = 3): DiffLine[] {
  const aLines = before.split('\n');
  const bLines = after.split('\n');

  // Build an edit script using the Myers diff algorithm (LCS approach).
  const lcs = buildLCS(aLines, bLines);
  const edits = buildEdits(aLines, bLines, lcs);

  // Expand each edit region with context lines.
  return expandContext(edits, aLines, bLines, contextLines);
}

// --- LCS / Myers diff internals ---

interface EditItem {
  type: 'equal' | 'insert' | 'delete';
  aIdx: number;  // index in aLines (for equal/delete)
  bIdx: number;  // index in bLines (for equal/insert)
}

function buildLCS(a: string[], b: string[]): number[][] {
  const m = a.length;
  const n = b.length;
  // dp[i][j] = length of LCS of a[0..i-1] and b[0..j-1]
  const dp: number[][] = Array.from({ length: m + 1 }, () => new Array(n + 1).fill(0));
  for (let i = 1; i <= m; i++) {
    for (let j = 1; j <= n; j++) {
      dp[i][j] = a[i - 1] === b[j - 1]
        ? dp[i - 1][j - 1] + 1
        : Math.max(dp[i - 1][j], dp[i][j - 1]);
    }
  }
  return dp;
}

function buildEdits(a: string[], b: string[], dp: number[][]): EditItem[] {
  const edits: EditItem[] = [];
  let i = a.length;
  let j = b.length;
  while (i > 0 || j > 0) {
    if (i > 0 && j > 0 && a[i - 1] === b[j - 1]) {
      edits.push({ type: 'equal', aIdx: i - 1, bIdx: j - 1 });
      i--; j--;
    } else if (j > 0 && (i === 0 || dp[i][j - 1] >= dp[i - 1][j])) {
      edits.push({ type: 'insert', aIdx: i, bIdx: j - 1 });
      j--;
    } else {
      edits.push({ type: 'delete', aIdx: i - 1, bIdx: j });
      i--;
    }
  }
  return edits.reverse();
}

function expandContext(
  edits: EditItem[],
  a: string[],
  b: string[],
  ctx: number,
): DiffLine[] {
  // Find indices into edits[] that are changed (insert/delete)
  const changedSet = new Set<number>();
  for (let k = 0; k < edits.length; k++) {
    if (edits[k].type !== 'equal') { changedSet.add(k); }
  }

  // Mark equal lines that are within `ctx` of a changed line
  const included = new Set<number>();
  for (const ci of changedSet) {
    for (let d = -ctx; d <= ctx; d++) {
      const idx = ci + d;
      if (idx >= 0 && idx < edits.length) { included.add(idx); }
    }
  }

  const result: DiffLine[] = [];
  let prevIncluded = false;
  for (let k = 0; k < edits.length; k++) {
    const edit = edits[k];
    if (!included.has(k)) {
      if (prevIncluded) {
        result.push({ type: 'context', lineNo: 0, text: '...' });
      }
      prevIncluded = false;
      continue;
    }
    prevIncluded = true;
    if (edit.type === 'equal') {
      result.push({ type: 'context', lineNo: edit.bIdx + 1, text: b[edit.bIdx] });
    } else if (edit.type === 'insert') {
      result.push({ type: 'added', lineNo: edit.bIdx + 1, text: b[edit.bIdx] });
    } else {
      result.push({ type: 'removed', lineNo: edit.aIdx + 1, text: a[edit.aIdx] });
    }
  }
  return result;
}
