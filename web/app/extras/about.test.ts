import { describe, expect, it } from 'vitest';

import { extras } from './index';

/**
 * Every extra this console ships says what it is before anyone turns it on:
 * Settings → Extra opens its preview from the tile, and a preview with nothing
 * in it is a button with no explanation beside it. The list is the one
 * `app/main.tsx` hands to `createApp`, imported rather than copied, so an extra
 * added there is held to this the day it ships.
 */
describe('every shipped extra has a preview', () => {
  it.each(extras.map((extra) => [extra.key, extra] as const))('%s says what it does, reads and needs', (_, extra) => {
    const about = extra.about;
    expect(about, `${extra.key} has no about`).toBeDefined();
    if (about === undefined) return;
    expect(about.description.trim()).not.toBe('');
    expect(about.reads.length).toBeGreaterThan(0);
    expect(about.permissions.length).toBeGreaterThan(0);
    for (const line of [...about.reads, ...about.permissions, ...about.requires]) {
      expect(line.trim()).not.toBe('');
    }
  });

  // The picture is its own `assets/preview.png`, drawn by `pnpm shots:extras`;
  // an extra shipped without one opens a preview with a hole where its page is.
  it.each(extras.map((extra) => [extra.key, extra] as const))('%s shows a picture of its page', (_, extra) => {
    expect(extra.about?.screenshot ?? '', `${extra.key} has no screenshot`).toMatch(/preview.*\.png/);
  });

  // A gallery's pictures are files of the extra's own assets, each captioned.
  it.each(extras.map((extra) => [extra.key, extra] as const))('%s captions every picture of its gallery', (_, extra) => {
    for (const picture of extra.about?.gallery ?? []) {
      expect(picture.src).toMatch(/\.png|^data:image/);
      expect(picture.caption.trim()).not.toBe('');
    }
  });

  it('covers the whole list', () => {
    expect(extras.length).toBeGreaterThan(0);
    expect(extras.filter((extra) => extra.about === undefined).map((extra) => extra.key)).toEqual([]);
  });
});
