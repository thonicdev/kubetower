import { expect, test } from '@playwright/test';

/* -------------------------------------------------------------------------- */
/* A container's image on a line of its own (#136)                             */
/* -------------------------------------------------------------------------- */

/**
 * `?scene=console-pod-images` (`pods.fixture.ts`'s `IMAGE_SHAPES`): a long
 * registry path, a digest, a tag and a digest, no version, and a 128-character
 * tag. At each width, for every tile:
 *
 * - the image's own name (its last path segment) shows at least four
 *   characters, so no tile is left with the version alone;
 * - nothing of the image line ends past the tile, and no tile scrolls sideways;
 * - the version and the copy button share one line;
 * - at 1440px every image but the 128-character tag fits on one line.
 */
for (const width of [1440, 1024, 390]) {
  test(`each container's image keeps its name readable and its line inside the tile, at ${String(width)}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1400 });
    await page.goto('/?scene=console-pod-images');
    await page.waitForSelector('[data-container] [data-image-line]');

    const tiles = await page.evaluate(() =>
      [...document.querySelectorAll<HTMLElement>('[data-container]')].map((tile) => {
        const box = tile.getBoundingClientRect();
        const line = tile.querySelector<HTMLElement>('[data-image-line]');
        const name = tile.querySelector<HTMLElement>('[data-image-name]');
        const badge = tile.querySelector<HTMLElement>('[data-image-version]');
        const copy = tile.querySelector<HTMLElement>('[data-image-line] button');
        if (line === null || name === null || badge === null || copy === null) throw new Error('an image line is incomplete');
        const charWidth = parseFloat(getComputedStyle(name).fontSize) * 0.6;
        const b = badge.getBoundingClientRect();
        const c = copy.getBoundingClientRect();
        return {
          container: tile.getAttribute('data-container') ?? '',
          nameChars: name.getBoundingClientRect().width / charWidth,
          past: Math.max(...[...line.querySelectorAll('*')].map((e) => e.getBoundingClientRect().right)) - box.right,
          sideways: tile.scrollWidth - tile.clientWidth,
          together: Math.abs((b.top + b.bottom) / 2 - (c.top + c.bottom) / 2) < 4,
          oneLine: line.getBoundingClientRect().height < 28,
        };
      }),
    );

    expect(tiles.length).toBe(5);
    for (const tile of tiles) {
      const at = `${String(width)}px, ${tile.container}`;
      expect(tile.nameChars, `${at}: the image's name shows ${tile.nameChars.toFixed(1)} characters`).toBeGreaterThanOrEqual(4);
      expect(tile.past, `${at}: the image line ends ${String(tile.past)}px past the tile`).toBeLessThanOrEqual(0.5);
      expect(tile.sideways, `${at}: the tile scrolls sideways`).toBeLessThanOrEqual(0);
      expect(tile.together, `${at}: the version and the copy are on two lines`).toBe(true);
      if (width === 1440 && tile.container !== 'nightly') expect(tile.oneLine, `${at}: the image takes two lines`).toBe(true);
    }
  });
}
