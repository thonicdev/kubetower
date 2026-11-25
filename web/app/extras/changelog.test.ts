import { existsSync, readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';

import { describe, expect, it } from 'vitest';

import { parseChangelog } from '../../ui/src/whatsnew/changelog';
import { extras } from './index';

/**
 * What's new draws an entry marked `<!-- extra: KEY -->` as a New extra line
 * (#121). A marker naming no shipped extra - a typo, a key that changed - would
 * draw a line nobody can open, so it is refused here, against the list
 * `app/main.tsx` ships (#125).
 */
describe("the changelog's extra markers", () => {
  // The walk to go.mod, as changelog.test.ts reads the contract.
  let root = resolve(process.cwd());
  while (!existsSync(join(root, 'go.mod')) && dirname(root) !== root) root = dirname(root);
  const text = readFileSync(join(root, 'CHANGELOG.md'), 'utf8');
  const marked = [...text.matchAll(/<!--\s*extra:\s*([^\s>]+?)\s*-->/g)].map((match) => match[1] ?? '');
  const read = parseChangelog(text).flatMap((release) => release.highlights.flatMap((highlight) => highlight.extra ?? []));

  it('marks at least the one 0.2.1 introduced', () => {
    expect(read).toContain('storage');
  });

  it('names a shipped extra, every one, and each reaches the dialog', () => {
    const shipped = new Set(extras.map((extra) => extra.key));
    for (const key of read) expect(shipped, `${key} is no shipped extra`).toContain(key);
    // None lost: not to the cut, not to a key the parser would not read.
    expect(read).toHaveLength(marked.length);
  });
});
