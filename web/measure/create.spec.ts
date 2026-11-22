import { expect, test } from '@playwright/test';
import { px } from './layoutkit';

/* -------------------------------------------------------------------------- */
/* 25 — the list's "+" and the selection bar's corner                       */
/* -------------------------------------------------------------------------- */

/**
 * The Pods list with its "+" permitted, at the phone width and a desktop one.
 *
 * - the "+" floats over the list's panel, at its bottom-right corner, inside
 *   the panel and the window, in no bar;
 * - once a row is ticked the selection bar takes that corner and the "+" is not
 *   drawn, so the two never overlap - and Clear brings it back.
 *
 * Numbers with names in the messages, no screenshot, as above.
 */
for (const width of [390, 1440]) {
  test(`the list's "+" floats over its panel's corner and gives the corner to the selection bar, at ${String(width)}`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 800 });
    await page.goto('/?scene=console-pods-create');
    const plus = page.getByRole('button', { name: 'Create a Pod' });
    await plus.waitFor();

    const at = await page.evaluate(() => {
      const panel = document.getElementById('panel');
      const button = document.querySelector('[data-control="create-object"]');
      if (panel === null || button === null) throw new Error('no panel or no "+"');
      const box = panel.getBoundingClientRect();
      const own = button.getBoundingClientRect();
      return {
        left: own.left - box.left,
        right: box.right - own.right,
        top: own.top - box.top,
        bottom: box.bottom - own.bottom,
        width: own.width,
        windowRight: window.innerWidth - own.right,
        windowBottom: window.innerHeight - own.bottom,
      };
    });
    console.log(
      `"+" at ${String(width)}: ${px(at.right)} from the panel's right, ${px(at.bottom)} from its foot, ${px(at.width)} wide`,
    );
    expect(at.width, 'the "+" is not drawn').toBeGreaterThan(0);
    expect(at.left, `the "+" is ${px(-at.left)} past the panel's left edge`).toBeGreaterThanOrEqual(0);
    expect(at.right, `the "+" is ${px(-at.right)} past the panel's right edge`).toBeGreaterThanOrEqual(0);
    // Over the panel's foot, not in a row under it.
    expect(at.bottom, `the "+" ends ${px(-at.bottom)} below the panel`).toBeGreaterThanOrEqual(0);
    expect(at.top, 'the "+" is above the panel').toBeGreaterThanOrEqual(0);
    await expect(page.locator('[data-create-row]'), 'a bar is drawn for the "+"').toHaveCount(0);
    expect(at.windowRight, `the "+" ends ${px(-at.windowRight)} past the window`).toBeGreaterThanOrEqual(0);
    expect(at.windowBottom, `the "+" ends ${px(-at.windowBottom)} below the window`).toBeGreaterThanOrEqual(0);
    // The corner, not somewhere inside: within the inset the selection bar uses.
    expect(at.right, 'the "+" is not in the right-hand corner').toBeLessThan(32);
    expect(at.windowBottom, 'the "+" is not at the foot').toBeLessThan(32);

    await page.getByRole('checkbox', { name: 'Select observability/prometheus-0' }).check();
    await page.getByText(/^[0-9]+ selected$/).waitFor();
    await expect(plus, 'the "+" is drawn under the selection bar').toHaveCount(0);

    await page.getByRole('button', { name: 'Clear' }).click();
    await expect(plus, 'Clear did not bring the "+" back').toBeVisible();
  });
}

/**
 * A list taller than its panel - the window short, as under an open dock. The
 * "+" floats, so a scroll does not move it; and the list keeps room after its
 * last row, so scrolled to the end, the "+" covers no cell of any row.
 */
for (const width of [390, 1440]) {
  test(`the list's "+" stays put and clears every row at the end of a list that fills the panel, at ${String(width)}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width < 600 ? 360 : 260 });
    await page.goto('/?scene=console-pods-create');
    const plusButton = page.getByRole('button', { name: 'Create a Pod' });
    await plusButton.waitFor();
    const before = await plusButton.boundingBox();
    await page.evaluate(() => {
      const panel = document.getElementById('panel');
      if (panel !== null) panel.scrollTop = panel.scrollHeight;
    });
    const after = await plusButton.boundingBox();
    expect(after?.y, 'the "+" moved with the scroll').toBe(before?.y);
    expect(after?.x, 'the "+" moved with the scroll').toBe(before?.x);

    const found = await page.evaluate(() => {
      const panel = document.getElementById('panel');
      const button = document.querySelector('[data-control="create-object"]');
      if (panel === null || button === null) throw new Error('no panel or no "+"');
      const plus = button.getBoundingClientRect();
      const box = panel.getBoundingClientRect();
      // Each cell as far as the panel shows it: a row scrolled out of view is
      // drawn nowhere, whatever its own box says.
      const cells = [...panel.querySelectorAll('td')]
        .map((cell) => cell.getBoundingClientRect())
        .map((cell) => ({
          left: Math.max(cell.left, box.left),
          right: Math.min(cell.right, box.right),
          top: Math.max(cell.top, box.top),
          bottom: Math.min(cell.bottom, box.bottom),
        }))
        .filter((cell) => cell.right > cell.left && cell.bottom > cell.top);
      const under = cells.filter(
        (cell) => cell.left < plus.right && cell.right > plus.left && cell.top < plus.bottom && cell.bottom > plus.top,
      ).length;
      return {
        overflows: panel.scrollHeight > panel.clientHeight,
        scrolled: panel.scrollTop > 0,
        cells: cells.length,
        under,
      };
    });
    expect(found.overflows, 'the rows do not fill the panel: the test measures nothing').toBe(true);
    expect(found.scrolled, 'the panel did not scroll').toBe(true);
    expect(found.cells, 'no cells drawn').toBeGreaterThan(0);
    expect(found.under, `the "+" covers ${String(found.under)} cell(s)`).toBe(0);
  });
}

/** The "+" is a finger's target under a coarse pointer, and 40px under a mouse. */
test.describe('under a coarse pointer', () => {
  test.use({ hasTouch: true, isMobile: true, viewport: { width: 390, height: 800 } });
  test('the list\'s "+" is 44px', async ({ page }) => {
    await page.goto('/?scene=console-pods-create');
    const box = await page.getByRole('button', { name: 'Create a Pod' }).boundingBox();
    expect(box?.width, 'the "+" is not 44px wide').toBe(44);
    expect(box?.height, 'the "+" is not 44px tall').toBe(44);
  });
});

test('the list\'s "+" is 40px under a mouse', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 800 });
  await page.goto('/?scene=console-pods-create');
  const box = await page.getByRole('button', { name: 'Create a Pod' }).boundingBox();
  expect(box?.width, 'the "+" is not 40px wide').toBe(40);
  expect(box?.height, 'the "+" is not 40px tall').toBe(40);
});
