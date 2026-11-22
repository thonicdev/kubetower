import { expect, test } from '@playwright/test';
import { px } from './layoutkit';

/* -------------------------------------------------------------------------- */
/* 11 — a long cluster name never wraps, and pushes nothing sideways         */
/* -------------------------------------------------------------------------- */

/**
 * A long cluster name must not wrap. Every surface that draws a cluster name goes
 * through `components/ClusterName.tsx`, which marks itself `data-cluster-name`;
 * this opens the whole console on `?scene=names` (`clusters.fixture.ts`: eight
 * clusters, the 89-character ARN on screen, its `_OWNER` twin,
 * `kubernetes-admin@kubernetes`) on the four pages that draw names, and reads
 * every one of them.
 *
 * One line is measured as a height: the name's box is no taller than one line
 * box of its own font. Inside its box is measured against the element that lays
 * it out, since the name cuts itself and so never overflows itself.
 */
const LONG_ARN =
  'arn:aws:eks:eu-west-3:123456789012:cluster/a-very-long-production-cluster-name-for-layout';
const NAME_PAGES: readonly (readonly [string, string])[] = [
  ['the overview', `/c/${encodeURIComponent(LONG_ARN)}`],
  ['the pods list', `/c/${encodeURIComponent(LONG_ARN)}/pods`],
  ['the cluster picker', '/'],
];

for (const width of [1280, 500]) {
  for (const [what, at] of NAME_PAGES) {
    test(`every cluster name on ${what} is one line inside its box at ${String(width)}px`, async ({ page }) => {
      await page.setViewportSize({ width, height: 900 });
      await page.goto(`/?scene=names&at=${encodeURIComponent(at)}`);
      await page.waitForSelector('[data-cluster-name]', { state: 'attached' });
      // Every answer is synchronous in the scene; a beat for the last query.
      await page.waitForTimeout(300);

      const reading = await page.evaluate(() =>
        ({
          names: [...document.querySelectorAll<HTMLElement>('[data-cluster-name]')].map((name) => {
            const box = name.getBoundingClientRect();
            const style = getComputedStyle(name);
            const fontSize = Number.parseFloat(style.fontSize);
            const lineHeight = Number.parseFloat(style.lineHeight) || fontSize * 1.5;
            const parent = name.parentElement?.getBoundingClientRect() ?? box;
            // What a hover shows: the innermost title above the element under
            // the pointer at the name's center. The name sets its own, except
            // where an enclosing element carries a better one (the crumb).
            //
            // **Whatever the pointer lands on, where the name has a box.** A
            // box laid over the name - a link stretched over a whole card -
            // must not pass on the name's own title when no hover shows it.
            // Only a name with no box, or one outside the viewport, falls back
            // to the name itself: there is nothing to point at.
            const pointed = document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 2);
            const under = pointed !== null && (name.contains(pointed) || box.width > 0) ? pointed : name;
            return {
              title: under.closest('[title]')?.getAttribute('title') ?? '',
              text: name.textContent ?? '',
              height: box.height,
              lineHeight,
              right: box.right,
              parentRight: parent.right,
              visible: box.width > 0,
            };
          }),
          scrollWidth: document.documentElement.scrollWidth,
          clientWidth: document.documentElement.clientWidth,
        }),
      );

      // Attached, and long: a page that drew no 89-character name would
      // measure nothing. Measured where it has a box at all - at 500px the
      // breadcrumb's crumb is squeezed to zero width by the header's controls,
      // and a zero-width box has no line to count.
      expect(
        reading.names.some((name) => name.text.length > 60),
        `${what} draws no long name, so it measures nothing`,
      ).toBe(true);
      const drawn = reading.names.filter((name) => name.visible);
      for (const name of drawn) {
        const label = name.text.length > 40 ? `${name.text.slice(0, 40)}…` : name.text;
        expect(
          name.height,
          `${label}: ${px(name.height)} tall against a ${px(name.lineHeight)} line - it wrapped`,
        ).toBeLessThanOrEqual(name.lineHeight * 1.5);
        expect(
          name.right,
          `${label}: its right edge is ${px(name.right)}, past its box's ${px(name.parentRight)}`,
        ).toBeLessThanOrEqual(name.parentRight + 0.5);
        expect(name.title, `${label}: the whole name is not on the tooltip a hover shows`).toContain(name.text);
      }
      expect(
        reading.scrollWidth - reading.clientWidth,
        `${what} scrolls sideways at ${String(width)}px: ${String(reading.scrollWidth)} against ${String(reading.clientWidth)}`,
      ).toBeLessThanOrEqual(0);
    });
  }
}

/* -------------------------------------------------------------------------- */
/* 12 — Arrange fits its width, and the color takes the narrow form         */
/* -------------------------------------------------------------------------- */

/**
 * Arrange must not scroll sideways. A table column is as wide as its widest
 * cell, so one ARN would set the dialog's width; Arrange is therefore a row per
 * cluster with a `minmax(0, 1fr)` middle, so the scroll box - the dialog's own, which is what
 * scrolls - must not be wider than itself, with realistic data: the eight
 * clusters of `clusters.fixture.ts`.
 *
 * And the narrow form: the sixteen swatches inline where
 * they fit, one color button where they do not. Both are in the DOM and the
 * breakpoint chooses; what is measured is which one has a box.
 */
for (const [width, inline] of [
  [1280, true],
  [500, false],
] as const) {
  test(`Arrange has no horizontal scroll at ${String(width)}px, with the color ${inline ? 'inline' : 'as one button'}`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.goto('/?scene=names');
    await page.locator('#rail button[aria-label="Arrange"]').click();
    const dialog = page.getByRole('dialog');
    await dialog.locator('li[data-cluster]').nth(7).waitFor();

    const reading = await dialog.evaluate((box) => {
      // The element that scrolls is the dialog's box itself (`overflow-auto`).
      const rows = [...box.querySelectorAll<HTMLElement>('li[data-cluster]')];
      const visible = (element: Element | null): boolean =>
        element !== null && element.getBoundingClientRect().width > 0;
      return {
        rows: rows.length,
        scrollWidth: box.scrollWidth,
        clientWidth: box.clientWidth,
        right: box.getBoundingClientRect().right,
        rowsRight: Math.max(...rows.map((row) => row.getBoundingClientRect().right)),
        grids: rows.filter((row) => visible(row.querySelector('[role="radiogroup"]'))).length,
        buttons: rows.filter((row) => visible(row.querySelector('[data-colour-button]'))).length,
        documentScrollWidth: document.documentElement.scrollWidth,
        documentClientWidth: document.documentElement.clientWidth,
      };
    });

    expect(reading.rows, 'Arrange draws a row per cluster').toBe(8);
    expect(
      reading.scrollWidth - reading.clientWidth,
      `Arrange scrolls sideways at ${String(width)}px: ${String(reading.scrollWidth)} against ${String(reading.clientWidth)}`,
    ).toBeLessThanOrEqual(0);
    expect(
      reading.rowsRight,
      `a row reaches ${px(reading.rowsRight)}, past the dialog's ${px(reading.right)}`,
    ).toBeLessThanOrEqual(reading.right + 0.5);
    expect(
      reading.documentScrollWidth - reading.documentClientWidth,
      `the page scrolls sideways under Arrange: ${String(reading.documentScrollWidth)} against ${String(reading.documentClientWidth)}`,
    ).toBeLessThanOrEqual(0);
    expect(reading.grids, `rows drawing the sixteen inline at ${String(width)}px`).toBe(inline ? 8 : 0);
    expect(reading.buttons, `rows drawing one color button at ${String(width)}px`).toBe(inline ? 0 : 8);
  });
}
