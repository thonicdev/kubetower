import { expect, test } from '@playwright/test';
import { VIEWPORTS, answerTheCluster, px } from './layoutkit';

/* -------------------------------------------------------------------------- */
/* 5 — a confirmation dialog does not overflow at 420px                      */
/* -------------------------------------------------------------------------- */

for (const scene of ['dialog', 'dialog-plain']) {
  test(`a confirmation dialog (${scene}) is intact at every viewport, and clamps to the veil at 420px`, async ({
    page,
  }) => {
    await answerTheCluster(page);

    for (const viewport of VIEWPORTS) {
      await page.setViewportSize(viewport);
      await page.goto(`/?scene=${scene}`);
      await page.waitForSelector('[role="dialog"]');

      const reading = await page.evaluate(() => {
        const box = document.querySelector('[role="dialog"]');
        if (!(box instanceof HTMLElement)) throw new Error('no dialog');
        return {
          scrollWidth: box.scrollWidth,
          clientWidth: box.clientWidth,
          scrollHeight: box.scrollHeight,
          clientHeight: box.clientHeight,
          width: box.getBoundingClientRect().width,
          documentScrollWidth: document.documentElement.scrollWidth,
          documentClientWidth: document.documentElement.clientWidth,
        };
      });

      const where = `${String(viewport.width)}x${String(viewport.height)}`;
      expect(
        reading.scrollWidth - reading.clientWidth,
        `${where}: the box overflows itself sideways — scrollWidth ${String(reading.scrollWidth)} against clientWidth ${String(reading.clientWidth)}`,
      ).toBeLessThanOrEqual(0);
      expect(
        reading.scrollHeight - reading.clientHeight,
        `${where}: the box overflows itself vertically — scrollHeight ${String(reading.scrollHeight)} against clientHeight ${String(reading.clientHeight)}`,
      ).toBeLessThanOrEqual(0);
      expect(
        reading.documentScrollWidth - reading.documentClientWidth,
        `${where}: the document scrolls sideways — ${String(reading.documentScrollWidth)} against ${String(reading.documentClientWidth)}`,
      ).toBeLessThanOrEqual(0);

      // The veil is `fixed inset-0 … p-4`, so the box's room is the viewport less
      // 16px each side. At 420 that is 388.
      const room = viewport.width - 32;
      expect(
        reading.width,
        `${where}: the box is ${px(reading.width)} where the veil leaves ${String(room)}px`,
      ).toBeLessThanOrEqual(room + 0.5);
    }
  });
}

/* -------------------------------------------------------------------------- */
/* 6 — the drawer's controls are on one arm, in one order                    */
/* -------------------------------------------------------------------------- */
