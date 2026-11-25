import { expect, test } from '@playwright/test';

/* -------------------------------------------------------------------------- */
/* Event Intelligence with names as long as Kubernetes allows (#34)          */
/* -------------------------------------------------------------------------- */

/**
 * `?scene=extra-events-long` (`events.fixture.ts`, `EVENTS_LONG_PAGE`): pods
 * named `<workload>-<rs hash>-<pod hash>`, past a hundred characters, in
 * 63-character namespaces. At each width:
 *
 * - the page draws no sideways scroll and nothing past its right edge;
 * - every object row is one line, its count and age whole;
 * - **every name keeps its end whole**, on the object rows and on the
 *   timeline's workload rows: the end's box lies inside the name's own box, which lies
 *   inside its row or lane, and the end is not clipped within itself. That
 *   end, the pod hash, is what tells two pods of one ReplicaSet apart. The
 *   timeline's rows are named by workload, and keep their end the same way.
 */
for (const width of [1440, 1024, 390]) {
  test(`long names keep the page in its width and every name its end, at ${String(width)}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.goto('/?scene=extra-events-long');
    await page.waitForSelector('[data-cause]');
    // Unfold the first cause: its objects are the rows with the longest names.
    await page.locator('[data-cause] > button').first().click();
    await page.waitForSelector('[data-group]');

    const reading = await page.evaluate((viewport) => {
      const doc = document.scrollingElement as HTMLElement;
      /** Where a name's end sits against the name's box and its container. */
      const ends = (container: HTMLElement) => {
        const name = container.querySelector<HTMLElement>('[data-cut-name]');
        const end = name?.querySelector<HTMLElement>('[data-cut-name-end]') ?? null;
        const box = name?.getBoundingClientRect();
        const outer = container.getBoundingClientRect();
        const endBox = end?.getBoundingClientRect();
        const line = name === null || name === undefined ? 0 : parseFloat(getComputedStyle(name).lineHeight);
        return {
          // One line: a name that wraps at its hyphens overlaps the next lane.
          oneLine: box === undefined || box.height <= line + 1,
          title: name?.getAttribute('title') ?? '',
          text: name?.textContent ?? '',
          end: end?.textContent ?? '',
          // The end is drawn whole: not clipped within itself, inside the
          // name's box, and the name's box inside its row or lane.
          endWhole: end === null || end.scrollWidth <= end.clientWidth + 0.5,
          endInName: endBox === undefined || box === undefined || (endBox.left >= box.left - 0.5 && endBox.right <= box.right + 0.5),
          nameInside: box === undefined || box.right <= outer.right + 0.5,
        };
      };
      const rows = [...document.querySelectorAll<HTMLElement>('[data-group]')].map((row) => {
        const count = row.querySelector<HTMLElement>('[data-group-count]');
        return {
          ...ends(row),
          height: row.getBoundingClientRect().height,
          line: parseFloat(getComputedStyle(row).lineHeight),
          countWhole: count !== null && count.scrollWidth <= count.clientWidth + 1,
          countInside: count !== null && count.getBoundingClientRect().right <= row.getBoundingClientRect().right + 1,
        };
      });
      const lanes = [...document.querySelectorAll<HTMLElement>('[data-workload]')].map(ends);
      const past = [...document.querySelectorAll<HTMLElement>('body *')].filter((e) => e.getBoundingClientRect().right > viewport + 1).length;
      return { scroll: doc.scrollWidth, client: doc.clientWidth, rows, lanes, past };
    }, width);

    expect(reading.scroll, 'the page scrolls sideways').toBe(reading.client);
    expect(reading.past, 'something is drawn past the right edge').toBe(0);
    expect(reading.rows.length).toBeGreaterThan(0);
    expect(reading.lanes.length).toBeGreaterThan(0);
    for (const row of reading.rows) {
      expect(row.height, 'an object row wraps').toBeLessThan(row.line * 2 + 8);
      expect(row.countWhole && row.countInside, 'a row cuts its count and age').toBe(true);
      expect(row.text).toBe(row.title);
      expect(row.end, `${row.title}: a pod keeps its ReplicaSet and pod hashes`).toMatch(/^-[0-9a-z]{9}-[0-9a-z]{5}$/);
      expect([row.endWhole, row.endInName, row.nameInside, row.oneLine], `${row.title}: its end is cut, or it wraps, on its row`).toEqual([true, true, true, true]);
    }
    for (const lane of reading.lanes) {
      expect(lane.text).toBe(lane.title);
      expect([lane.endWhole, lane.endInName, lane.nameInside, lane.oneLine], `${lane.title}: its end is cut, or it wraps, on its lane`).toEqual([true, true, true, true]);
    }
  });
}
