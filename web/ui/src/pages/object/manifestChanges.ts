/**
 * What the manifest editor counts as a change, and where each one is.
 *
 * A **change** is one hunk of a line diff of the edited document against the
 * one the editor opened with: a run of consecutive lines that were removed,
 * added, or both, with no unchanged line inside it. Three edits far apart are
 * three changes; two neighboring lines edited together are one.
 *
 * A change's **changed lines** are the larger of what it removed and what it
 * added, so a line edited in place counts once (not as a removal plus an
 * addition), a line inserted counts once, and a line deleted counts once.
 *
 * ## Why a diff written here
 *
 * The bundle has no diff library - the editor (`ManifestCode.tsx`) is
 * CodeMirror without its merge view - and the review's diff is the server's
 * (`PlanFacts.diff`), computed only on *Review changes*. A count that follows
 * every keystroke cannot wait for a dry run, and a shortest edit script
 * over lines is a few dozen lines - not worth a dependency.
 *
 * The lines both documents share at the start and at the end are set aside
 * first, and what is left is compared with Myers' algorithm, whose cost grows
 * with the size of the edit rather than of the document. Past `LIMIT` cells of
 * its trace - about 1,400 lines of edit script, which a handful of edits never
 * reaches and a paste that rewrote most of a large document does - the middle
 * is reported as a single change marked `approximate`: it says where the
 * changed stretch is, but neither how many changes it holds nor how many of its
 * lines changed, so the editor says it did not count rather than show a figure
 * it did not compute.
 */

/** One change: where it sits in the edited document, and its size. */
export interface Hunk {
  /** Its first line in the edited document, from 0. For a change that only
   *  removed lines, the line that now follows the removal. */
  readonly line: number;
  readonly removed: number;
  readonly added: number;
  /** The middle was too large to compare: one block standing for every
   *  change in it, and its sizes are the stretch's, not a count of changes. */
  readonly approximate?: true;
}

/** Above this many cells of Myers' trace, the middle is one change. */
const LIMIT = 2_000_000;

/** The changes of `after` against `before`, in document order. */
export function lineHunks(before: string, after: string): Hunk[] {
  if (before === after) return [];
  const a = before.split('\n');
  const b = after.split('\n');

  let start = 0;
  while (start < a.length && start < b.length && a[start] === b[start]) start++;
  let endA = a.length;
  let endB = b.length;
  while (endA > start && endB > start && a[endA - 1] === b[endB - 1]) {
    endA--;
    endB--;
  }

  const n = endA - start;
  const m = endB - start;
  if (n === 0 && m === 0) return [];
  if (n === 0 || m === 0) {
    return [{ line: start, removed: n, added: m }];
  }

  const ops = myers(a.slice(start, endA), b.slice(start, endB));
  if (ops === undefined) return [{ line: start, removed: n, added: m, approximate: true }];

  const hunks: Hunk[] = [];
  let line = start;
  let hunk: { line: number; removed: number; added: number } | undefined;
  for (const op of ops) {
    if (op === '=') {
      if (hunk !== undefined) hunks.push(hunk);
      hunk = undefined;
      line++;
      continue;
    }
    hunk ??= { line, removed: 0, added: 0 };
    if (op === '-') {
      hunk.removed++;
    } else {
      hunk.added++;
      line++;
    }
  }
  if (hunk !== undefined) hunks.push(hunk);
  return hunks;
}

/**
 * Myers' shortest edit script of `b` from `a`, as one operation per line in
 * document order: `=` kept, `-` removed from `a`, `+` added from `b`.
 * `undefined` when the trace it keeps would pass `LIMIT` cells.
 *
 * Its cost grows with the size of the edit, not of the document, which is
 * what a handful of edits in a long manifest needs. The trace keeps, for each
 * step `d`, only the `2d + 1` diagonals that step can reach, so its size is
 * about `d²` whatever the document's length: thirty one-line edits scattered
 * through 10,000 lines keep a few thousand cells.
 */
function myers(a: readonly string[], b: readonly string[]): ('=' | '-' | '+')[] | undefined {
  const n = a.length;
  const m = b.length;
  const max = n + m;
  // v[k + max]: the furthest x reached on diagonal k.
  const v = new Int32Array(2 * max + 1);
  // trace[d][k + d]: v on diagonal k once step d is done, for -d <= k <= d.
  const trace: Int32Array[] = [];
  let kept = 0;
  let found = false;
  for (let d = 0; d <= max && !found; d++) {
    for (let k = -d; k <= d; k += 2) {
      const down = k === -d || (k !== d && (v[k - 1 + max] ?? 0) < (v[k + 1 + max] ?? 0));
      let x = down ? (v[k + 1 + max] ?? 0) : (v[k - 1 + max] ?? 0) + 1;
      let y = x - k;
      while (x < n && y < m && a[x] === b[y]) {
        x++;
        y++;
      }
      v[k + max] = x;
      if (x >= n && y >= m) {
        found = true;
        break;
      }
    }
    kept += 2 * d + 1;
    if (kept > LIMIT) return undefined;
    trace.push(v.slice(max - d, max + d + 1));
  }

  const ops: ('=' | '-' | '+')[] = [];
  let x = n;
  let y = m;
  for (let d = trace.length - 1; d > 0; d--) {
    // Where step d started from: the diagonals once step d - 1 was done.
    const before = trace[d - 1];
    const at = (k: number): number => before?.[k + d - 1] ?? 0;
    const k = x - y;
    const down = k === -d || (k !== d && at(k - 1) < at(k + 1));
    const prevK = down ? k + 1 : k - 1;
    const prevX = at(prevK);
    const prevY = prevX - prevK;
    while (x > prevX && y > prevY) {
      ops.push('=');
      x--;
      y--;
    }
    ops.push(down ? '+' : '-');
    x = prevX;
    y = prevY;
  }
  while (x > 0 && y > 0) {
    ops.push('=');
    x--;
    y--;
  }
  return ops.reverse();
}

/** The changed lines of every change together (the module header defines
 *  them). Meaningless when a change is `approximate`: the caller says so. */
export function changedLines(hunks: readonly Hunk[]): number {
  return hunks.reduce((sum, hunk) => sum + Math.max(hunk.removed, hunk.added), 0);
}

/**
 * The change one step from `at` among `count`, wrapping at both ends, so that
 * ↓ on the last goes back to the first - and with a single change, either
 * arrow still goes to it.
 */
export function stepChange(at: number, by: 1 | -1, count: number): number {
  if (count <= 0) return 0;
  return (((at + by) % count) + count) % count;
}

/**
 * Where a change's line starts in `text`, as a character offset - what the
 * textarea's caret is set to. A line past the end is the end.
 */
export function lineOffset(text: string, line: number): number {
  let offset = 0;
  for (let at = 0; at < line; at++) {
    const next = text.indexOf('\n', offset);
    if (next === -1) return text.length;
    offset = next + 1;
  }
  return offset;
}
