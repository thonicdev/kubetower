import { describe, expect, it } from 'vitest';

import { changedLines, type Hunk, lineHunks, lineOffset, stepChange } from './manifestChanges';

const ORIGINAL = ['a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i', ''].join('\n');

describe('lineHunks', () => {
  it('finds nothing in an unchanged document', () => {
    expect(lineHunks(ORIGINAL, ORIGINAL)).toEqual([]);
  });

  it('makes one change of a line edited in place, counted once', () => {
    const hunks = lineHunks(ORIGINAL, ORIGINAL.replace('c', 'C'));
    expect(hunks).toEqual([{ line: 2, removed: 1, added: 1 }]);
    expect(changedLines(hunks)).toBe(1);
  });

  it('makes three changes of three edits apart, in document order', () => {
    const after = ORIGINAL.replace('b', 'B').replace('e', 'E').replace('h', 'H');
    const hunks = lineHunks(ORIGINAL, after);
    expect(hunks.map((hunk) => hunk.line)).toEqual([1, 4, 7]);
    expect(changedLines(hunks)).toBe(3);
  });

  it('makes one change of neighboring lines edited together', () => {
    const hunks = lineHunks(ORIGINAL, ORIGINAL.replace('c\nd', 'C\nD'));
    expect(hunks).toEqual([{ line: 2, removed: 2, added: 2 }]);
    expect(changedLines(hunks)).toBe(2);
  });

  it('counts an insertion and a removal by their lines, and places each', () => {
    const inserted = lineHunks(ORIGINAL, ORIGINAL.replace('d\n', 'd\nx\ny\n'));
    expect(inserted).toEqual([{ line: 4, removed: 0, added: 2 }]);
    expect(changedLines(inserted)).toBe(2);

    const removed = lineHunks(ORIGINAL, ORIGINAL.replace('f\ng\n', ''));
    // The line that now follows the removal: `h`, the sixth line.
    expect(removed).toEqual([{ line: 5, removed: 2, added: 0 }]);
    expect(changedLines(removed)).toBe(2);
  });

  it('places a block of five lines inserted or removed in the middle', () => {
    const inserted = lineHunks(ORIGINAL, ORIGINAL.replace('d\n', 'd\nv\nw\nx\ny\nz\n'));
    expect(inserted).toEqual([{ line: 4, removed: 0, added: 5 }]);
    expect(changedLines(inserted)).toBe(5);

    const removed = lineHunks(ORIGINAL, ORIGINAL.replace('c\nd\ne\nf\ng\n', ''));
    // The line that now follows the removal: `h`, the third line.
    expect(removed).toEqual([{ line: 2, removed: 5, added: 0 }]);
    expect(changedLines(removed)).toBe(5);
  });

  it('counts an edit that grew a line into three as three', () => {
    const hunks = lineHunks(ORIGINAL, ORIGINAL.replace('e', 'e1\ne2\ne3'));
    expect(changedLines(hunks)).toBe(3);
    expect(hunks).toHaveLength(1);
  });

  it('tells a moved line from an edit: a removal and an insertion, two changes', () => {
    const after = ['a', 'c', 'd', 'b', 'e', 'f', 'g', 'h', 'i', ''].join('\n');
    const hunks = lineHunks(ORIGINAL, after);
    expect(hunks).toHaveLength(2);
    expect(changedLines(hunks)).toBe(2);
  });

  it('sees a change at the very first and the very last line', () => {
    const after = ORIGINAL.replace('a', 'A') + 'tail';
    expect(lineHunks(ORIGINAL, after).map((hunk) => hunk.line)).toEqual([0, 9]);
  });

  it('marks a rewrite too large to compare as one approximate change', () => {
    const big = Array.from({ length: 1500 }, (_, at) => `line ${String(at)}`).join('\n');
    const rewritten = Array.from({ length: 1500 }, (_, at) => `other ${String(at)}`).join('\n');
    expect(lineHunks(big, rewritten)).toEqual([
      { line: 0, removed: 1500, added: 1500, approximate: true },
    ]);
  });

  it('counts thirty edits scattered through 10,000 lines exactly, one change each', () => {
    const lines = Array.from({ length: 10_000 }, (_, at) => `line ${String(at)}`);
    const after = [...lines];
    const edited = Array.from({ length: 30 }, (_, at) => 150 + at * 330);
    for (const at of edited) after[at] = `edited ${String(at)}`;
    const hunks = lineHunks(lines.join('\n'), after.join('\n'));
    expect(hunks.map((hunk) => hunk.line)).toEqual(edited);
    expect(hunks.every((hunk) => hunk.approximate === undefined)).toBe(true);
    expect(changedLines(hunks)).toBe(30);
  });

  it('compares only the middle of a large document edited in two places', () => {
    const lines = Array.from({ length: 5000 }, (_, at) => `line ${String(at)}`);
    const after = [...lines];
    after[100] = 'edited';
    after[4900] = 'edited too';
    const hunks = lineHunks(lines.join('\n'), after.join('\n'));
    expect(hunks.map((hunk) => hunk.line)).toEqual([100, 4900]);
  });
});

/**
 * A small seeded fuzz of `lineHunks` against a brute-force reference.
 *
 * Two shortest edit scripts of the same documents can place their hunks
 * differently, so the hunks are not compared with one particular script's.
 * What every correct answer shares is checked instead: the hunks rebuild
 * `after` from `before` (the lines between them are kept, in order), each is
 * maximal and non-empty, and together they remove `n - lcs` lines and add
 * `m - lcs`, the longest common subsequence being computed by the textbook
 * table. A wrong branch in Myers' backtrack - its `k === -d` edge, say -
 * gives hunks that fail one of these on short documents.
 */
describe('lineHunks against a brute-force LCS', () => {
  /** mulberry32: a few lines, and the same sequence on every run. */
  function random(seed: number): () => number {
    let state = seed >>> 0;
    return () => {
      state = (state + 0x6d2b79f5) >>> 0;
      let t = state;
      t = Math.imul(t ^ (t >>> 15), t | 1);
      t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
      return ((t ^ (t >>> 14)) >>> 0) / 4_294_967_296;
    };
  }

  function lcs(a: readonly string[], b: readonly string[]): number {
    const row = new Array<number>(b.length + 1).fill(0);
    for (const line of a) {
      let diagonal = 0;
      for (let j = 1; j <= b.length; j++) {
        const above = row[j] ?? 0;
        row[j] = line === b[j - 1] ? diagonal + 1 : Math.max(above, row[j - 1] ?? 0);
        diagonal = above;
      }
    }
    return row[b.length] ?? 0;
  }

  /** Why the hunks are not a shortest edit of `before` into `after`, or
   *  `undefined` when they are. */
  function wrong(before: string, after: string, hunks: readonly Hunk[]): string | undefined {
    const a = before.split('\n');
    const b = after.split('\n');
    let x = 0;
    let y = 0;
    for (const [at, hunk] of hunks.entries()) {
      if (hunk.approximate) return `hunk ${String(at)} is approximate`;
      if (hunk.removed + hunk.added === 0) return `hunk ${String(at)} is empty`;
      const kept = hunk.line - y;
      if (kept < 0 || (at > 0 && kept === 0)) return `hunk ${String(at)} overlaps or touches the last`;
      for (let i = 0; i < kept; i++) {
        if (a[x + i] !== b[y + i]) return `line ${String(y + i)} is kept but differs`;
      }
      x += kept + hunk.removed;
      y += kept + hunk.added;
    }
    if (a.length - x !== b.length - y) return 'the hunks do not end both documents together';
    for (let i = 0; x + i < a.length; i++) {
      if (a[x + i] !== b[y + i]) return `line ${String(y + i)} is kept but differs`;
    }
    const common = lcs(a, b);
    const removed = hunks.reduce((sum, hunk) => sum + hunk.removed, 0);
    const added = hunks.reduce((sum, hunk) => sum + hunk.added, 0);
    if (removed !== a.length - common || added !== b.length - common) {
      return `-${String(removed)} +${String(added)}, the shortest is -${String(a.length - common)} +${String(b.length - common)}`;
    }
    return undefined;
  }

  it('gives a shortest edit on 2,000 random short documents and edits', () => {
    const next = random(376);
    const pick = (count: number): number => Math.floor(next() * count);
    const alphabet = ['a', 'b', 'c', 'd', ''];
    for (let run = 0; run < 2_000; run++) {
      const before = Array.from({ length: pick(13) }, () => alphabet[pick(alphabet.length)] ?? '');
      const after = [...before];
      // A third of the runs make one edit only, so a block inserted or
      // removed alone - the path that skips Myers - is reached often.
      const edits = pick(3) === 0 ? 1 : 1 + pick(4);
      for (let edit = 0; edit < edits; edit++) {
        const at = pick(after.length + 1);
        const line = alphabet[pick(alphabet.length)] ?? '';
        // A block of 2 to 6 adjacent lines, inserted or removed at once.
        const block = 2 + pick(5);
        const kind = pick(5);
        if (kind === 0) after.splice(at, 0, line);
        else if (kind === 1) after.splice(at, 1);
        else if (kind === 2) after.splice(at, 1, line);
        else if (kind === 3) {
          const lines = Array.from({ length: block }, () => alphabet[pick(alphabet.length)] ?? '');
          after.splice(at, 0, ...lines);
        } else after.splice(at, block);
      }
      const a = before.join('\n');
      const b = after.join('\n');
      const reason = wrong(a, b, lineHunks(a, b));
      expect(reason, `run ${String(run)}: ${JSON.stringify(a)} -> ${JSON.stringify(b)}`).toBeUndefined();
    }
  });
});

describe('stepChange', () => {
  it('steps forward and back, and wraps at both ends', () => {
    expect(stepChange(0, 1, 3)).toBe(1);
    expect(stepChange(1, -1, 3)).toBe(0);
    expect(stepChange(2, 1, 3)).toBe(0);
    expect(stepChange(0, -1, 3)).toBe(2);
  });

  it('stays on a single change, and on nothing when there is none', () => {
    expect(stepChange(0, 1, 1)).toBe(0);
    expect(stepChange(0, -1, 1)).toBe(0);
    expect(stepChange(0, 1, 0)).toBe(0);
  });
});

describe('lineOffset', () => {
  it('is the start of the line, and the end past the last one', () => {
    expect(lineOffset('ab\ncd\nef', 0)).toBe(0);
    expect(lineOffset('ab\ncd\nef', 2)).toBe(6);
    expect(lineOffset('ab\ncd', 9)).toBe(5);
  });
});
