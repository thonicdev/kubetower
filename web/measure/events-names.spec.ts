import { expect, test } from '@playwright/test';

/* -------------------------------------------------------------------------- */
/* Event Intelligence with names as long as Kubernetes allows (#34)          */
/* -------------------------------------------------------------------------- */

/**
 * `?scene=extra-events-long` (`events.fixture.ts`, `EVENTS_LONG_PAGE`): pods
 * whose names run past a hundred characters in 63-character namespaces. At each
 * width the page draws no sideways scroll, every object row is one line with
 * its count and age whole, and every name keeps its distinguishing end.
 */
for (const width of [1440, 1024, 390]) {
  test(`long names keep the page in its width, one line per object, at ${String(width)}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.goto('/?scene=extra-events-long');
    await page.waitForSelector('[data-cause]');
    // Unfold the first cause: its objects are the rows with the longest names.
    await page.locator('[data-cause] > button').first().click();
    await page.waitForSelector('[data-group]');

    const reading = await page.evaluate((viewport) => {
      const doc = document.scrollingElement as HTMLElement;
      const rows = [...document.querySelectorAll<HTMLElement>('[data-group]')].map((row) => {
        const count = row.querySelector<HTMLElement>('[data-group-count]');
        const name = row.querySelector<HTMLElement>('[data-cut-name]');
        const line = parseFloat(getComputedStyle(row).lineHeight);
        return {
          height: row.getBoundingClientRect().height,
          line,
          countWhole: count !== null && count.scrollWidth <= count.clientWidth + 1,
          countInside: count !== null && count.getBoundingClientRect().right <= row.getBoundingClientRect().right + 1,
          title: name?.getAttribute('title') ?? '',
          text: name?.textContent ?? '',
          end: name?.lastElementChild?.getBoundingClientRect().right ?? 0,
          right: row.getBoundingClientRect().right,
        };
      });
      const past = [...document.querySelectorAll<HTMLElement>('body *')].filter((e) => e.getBoundingClientRect().right > viewport + 1).length;
      return { scroll: doc.scrollWidth, client: doc.clientWidth, rows, past };
    }, width);

    expect(reading.scroll, 'the page scrolls sideways').toBe(reading.client);
    expect(reading.past, 'something is drawn past the right edge').toBe(0);
    expect(reading.rows.length).toBeGreaterThan(0);
    for (const row of reading.rows) {
      expect(row.height, 'an object row wraps').toBeLessThan(row.line * 2 + 8);
      expect(row.countWhole && row.countInside, 'a row cuts its count and age').toBe(true);
      // The whole name is its text and its tooltip; its end is drawn.
      expect(row.text).toBe(row.title);
      expect(row.end).toBeLessThanOrEqual(row.right);
    }
  });
}
