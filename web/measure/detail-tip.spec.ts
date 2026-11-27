import { expect, test, type Page } from '@playwright/test';

/* -------------------------------------------------------------------------- */
/* A detail tip, opened, stays readable on a phone (#155)                      */
/* -------------------------------------------------------------------------- */

/**
 * Each `ⓘ` of a scene is pressed in turn, and its panel is measured open:
 *
 * - it lies inside the window, with nothing of it past an edge;
 * - its text is not cut: the panel scrolls neither way;
 * - nothing covers it or clips it: the point at each of its corners, just
 *   inside, is the panel.
 *
 * Scenes: an image of each shape (`console-pod-images`), and a No metrics
 * reason (`overview-no-prometheus`).
 *
 * And an image line is as tall with an `ⓘ` as without one.
 */
async function tips(page: Page, width: number): Promise<{ at: string; left: number; right: number; cut: boolean; covered: boolean }[]> {
  const out = [];
  const triggers = page.locator('[data-detail-tip] button');
  const count = await triggers.count();
  expect(count, 'the scene has no detail tip').toBeGreaterThan(0);
  for (let i = 0; i < count; i += 1) {
    const trigger = triggers.nth(i);
    await trigger.scrollIntoViewIfNeeded();
    await trigger.click();
    const panel = page.locator('[data-detail-tip-text]');
    await expect(panel).toHaveCount(1);
    out.push(
      await panel.evaluate((element, w) => {
        const box = element.getBoundingClientRect();
        const inside = (x: number, y: number): boolean => {
          const hit = document.elementFromPoint(x, y);
          return hit !== null && (hit === element || element.contains(hit));
        };
        const corners: [number, number][] = [
          [box.left + 2, box.top + 2],
          [box.right - 2, box.top + 2],
          [box.left + 2, box.bottom - 2],
          [box.right - 2, box.bottom - 2],
        ];
        return {
          at: `${String(w)}px, ${element.textContent.slice(0, 24)}`,
          left: box.left,
          right: box.right - w,
          cut: element.scrollWidth > element.clientWidth || element.scrollHeight > element.clientHeight,
          covered: !corners.every(([x, y]) => inside(x, y)),
        };
      }, width),
    );
    await page.keyboard.press('Escape');
    await expect(panel).toHaveCount(0);
  }
  return out;
}

for (const width of [390, 320]) {
  test(`an image's version tip opens inside the window and uncut, at ${String(width)}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1400 });
    await page.goto('/?scene=console-pod-images');
    await page.waitForSelector('[data-container] [data-image-line]');
    for (const tip of await tips(page, width)) {
      expect(tip.left, `${tip.at}: the tip starts past the left edge`).toBeGreaterThanOrEqual(0);
      expect(tip.right, `${tip.at}: the tip ends past the right edge`).toBeLessThanOrEqual(0);
      expect(tip.cut, `${tip.at}: the tip's text is cut`).toBe(false);
      expect(tip.covered, `${tip.at}: something covers or clips the tip`).toBe(false);
    }
  });
}

test('an image line is as tall with a version tip as without one', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1400 });
  await page.goto('/?scene=console-pod-images');
  await page.waitForSelector('[data-container] [data-image-line]');
  const lines = await page.evaluate(() =>
    [...document.querySelectorAll<HTMLElement>('[data-container]')]
      // The 128-character tag takes two lines at any width.
      .filter((tile) => tile.getAttribute('data-container') !== 'nightly')
      .map((tile) => {
        const line = tile.querySelector<HTMLElement>('[data-image-line]');
        return { tip: line?.querySelector('[data-detail-tip]') !== null, height: line?.getBoundingClientRect().height ?? 0 };
      }),
  );
  const withTip = lines.filter((one) => one.tip).map((one) => one.height);
  const without = lines.filter((one) => !one.tip).map((one) => one.height);
  expect(withTip.length).toBeGreaterThan(0);
  expect(without.length).toBeGreaterThan(0);
  for (const height of [...withTip, ...without]) expect(height).toBe(without[0]);
});

for (const width of [390, 320]) {
  test(`a No metrics reason opens inside the window and uncut, at ${String(width)}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.goto('/?scene=overview-no-prometheus');
    await page.waitForSelector('[data-no-metrics] [data-detail-tip]');
    for (const tip of await tips(page, width)) {
      expect(tip.left, `${tip.at}: the tip starts past the left edge`).toBeGreaterThanOrEqual(0);
      expect(tip.right, `${tip.at}: the tip ends past the right edge`).toBeLessThanOrEqual(0);
      expect(tip.cut, `${tip.at}: the tip's text is cut`).toBe(false);
      expect(tip.covered, `${tip.at}: something covers or clips the tip`).toBe(false);
    }
  });
}
