import { expect, test } from '@playwright/test';

/* -------------------------------------------------------------------------- */
/* The Network Inspector's enlarged pod view (#16)                            */
/* -------------------------------------------------------------------------- */

/**
 * `?scene=extra-netinspect-tab` (`netinspect.fixture.ts`), the small graph
 * pressed: the dialog with the large graph, the rules and the observed flows.
 * At each width:
 *
 * - neither the page nor the dialog scrolls sideways, flows table included;
 * - every text of the graph is drawn at 9px or more, however narrow the
 *   window: the graph is laid out at the width it has, not shrunk to it.
 */
for (const width of [1440, 1024, 390]) {
  test(`the enlarged pod view stays in its width and its graph text readable, at ${String(width)}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.goto('/?scene=extra-netinspect-tab');
    await page.locator('[data-enlarge]').click();
    await page.waitForSelector('[data-pod-enlarged] [data-pod-flows]');

    const reading = await page.evaluate(() => {
      const doc = document.scrollingElement as HTMLElement;
      const dialog = document.querySelector<HTMLElement>('[role="dialog"]');
      const svg = document.querySelector<SVGSVGElement>('[data-pod-enlarged] [data-large-graph]');
      if (dialog === null || svg === null) throw new Error('no dialog or no graph');
      const scale = svg.getBoundingClientRect().width / svg.viewBox.baseVal.width;
      const smallest = Math.min(...[...svg.querySelectorAll('text')].map((t) => parseFloat(t.getAttribute('font-size') ?? '10') * scale));
      const table = document.querySelector<HTMLElement>('[data-pod-enlarged] [data-pod-flows]');
      return {
        doc: doc.scrollWidth - doc.clientWidth,
        dialog: dialog.scrollWidth - dialog.clientWidth,
        table: table === null ? 0 : table.getBoundingClientRect().right - dialog.getBoundingClientRect().right,
        smallest,
      };
    });

    expect(reading.doc, `${String(width)}px: the page scrolls sideways by ${String(reading.doc)}px`).toBeLessThanOrEqual(0);
    expect(reading.dialog, `${String(width)}px: the dialog scrolls sideways by ${String(reading.dialog)}px`).toBeLessThanOrEqual(0);
    expect(reading.table, `${String(width)}px: the flows table ends ${String(reading.table)}px past the dialog`).toBeLessThanOrEqual(0.5);
    expect(reading.smallest, `${String(width)}px: a graph text is drawn at ${reading.smallest.toFixed(1)}px`).toBeGreaterThanOrEqual(9);
  });
}
