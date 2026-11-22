import { expect, test } from '@playwright/test';
import { verticalScrollers } from './layoutkit';

/* -------------------------------------------------------------------------- */
/* 21 — one scroll bar, not two, when a pod is opened                        */
/* -------------------------------------------------------------------------- */

const SCROLL_VIEWPORTS = [
  { width: 1024, height: 600 },
  { width: 1024, height: 768 },
  { width: 1280, height: 600 },
  { width: 1280, height: 720 },
  { width: 1440, height: 900 },
  { width: 1920, height: 1080 },
];

for (const [scene, what, narrowed] of [
  ['console-pod-tall-open', 'in the drawer', false],
  ['console-pod-tall-page', 'as a page', false],
  // The drawer narrowed as far as it goes: a stored width under the floor,
  // drawn at the floor.
  ['console-pod-tall-open', 'in a drawer narrowed to its floor', true],
] as const) {
  test(`a pod taller than the window scrolls its own panel and never the page, ${what}`, async ({ page }) => {
    if (narrowed) {
      await page.addInitScript(() => {
        localStorage.setItem('kubetower.drawer.width', '200');
      });
    }
    await page.setViewportSize({ width: 1280, height: 720 });
    await page.goto(`/?scene=${scene}`);
    // The last port's copy button: the live region that made the page grow.
    await page.locator('#object-panel-details li').filter({ hasText: '9097' }).getByRole('button', { name: 'Copy' }).waitFor();
    if (scene === 'console-pod-tall-open') await page.locator('#panel tbody tr').nth(39).waitFor();

    for (const viewport of SCROLL_VIEWPORTS) {
      await page.setViewportSize(viewport);
      const reading = await verticalScrollers(page);
      const at = `${String(viewport.width)}x${String(viewport.height)}`;
      console.log(`a tall pod ${what} at ${at}: document ${String(reading.document)}/${String(reading.window)}, scrolling ${reading.scrollers.join(', ')}`);
      expect(
        reading.document,
        `at ${at} the document is ${String(reading.document)}px in a ${String(reading.window)}px window: a second scroll bar`,
      ).toBeLessThanOrEqual(reading.window);
      expect(reading.scrollers, `at ${at} the pod's tab panel does not scroll, so the case is not measured`).toContain(
        '#object-panel-details',
      );
      for (const scroller of reading.scrollers) {
        expect(
          ['#object-panel-details', '#panel'],
          `at ${at} ${scroller} scrolls as well as the panel it is inside`,
        ).toContain(scroller);
      }
      if (narrowed) {
        const drawn = await page.evaluate(() => document.getElementById('drawer')?.getBoundingClientRect().width ?? 0);
        expect(drawn, `at ${at} the drawer is not narrowed, so the case is not measured`).toBeLessThan(
          Math.min(560, viewport.width * 0.46) - 1,
        );
      }
    }
  });
}

/**
 * The shell stays where it is. On the whole console, in a
 * window too short for the page, a wheel pushed hard at both ends of the content
 * moves only `#page`: the top bar and the rail do not move and the document's own
 * scroll position stays 0.
 *
 * **What this does and does not pin.** The elastic bounce itself is not drawn by
 * headless Chromium, so no measurement here can see it. The scroll-position
 * assertions hold on `main` too (the shell's `h-dvh overflow-hidden` already
 * keeps the document at 0); what fails without the change are the three
 * `overscroll-behavior` assertions, which pin the declarations that stop the
 * scroll chaining to the document and the bounce on a device that draws it. It
 * is a guard on those declarations, not a proof of the bounce.
 */
test('only the content area scrolls: the bars stay, the document stays at 0 and cannot bounce', async ({
  page,
}) => {
  await page.setViewportSize({ width: 1280, height: 300 });
  await page.goto('/?scene=overview');
  await page.locator('#rail button[aria-label="Log out"]').waitFor();
  // The overview draws its cards as its queries answer: wait for it to be long.
  await page.waitForFunction(() => {
    const main = document.getElementById('page');
    return main !== null && main.scrollHeight - main.clientHeight > 50;
  });

  const read = () =>
    page.evaluate(() => {
      const top = (selector: string) => {
        const element = document.querySelector(selector);
        if (!(element instanceof HTMLElement)) throw new Error(`no ${selector}`);
        return element.getBoundingClientRect().top;
      };
      const main = document.getElementById('page');
      if (main === null) throw new Error('no #page');
      const root = document.scrollingElement ?? document.documentElement;
      return {
        topbar: top('#topbar'),
        rail: top('#rail'),
        content: main.scrollTop,
        room: main.scrollHeight - main.clientHeight,
        document: root.scrollTop,
        documentRoom: root.scrollHeight - root.clientHeight,
        html: getComputedStyle(document.documentElement).overscrollBehaviorY,
        body: getComputedStyle(document.body).overscrollBehaviorY,
        main: getComputedStyle(main).overscrollBehaviorY,
      };
    });

  const before = await read();
  expect(before.room, 'the scene must be longer than the window').toBeGreaterThan(50);

  await page.mouse.move(700, 150);
  await page.mouse.wheel(0, 5000);
  await page.mouse.wheel(0, 5000);
  const bottom = await read();
  await page.mouse.wheel(0, -9000);
  await page.mouse.wheel(0, -9000);
  const top = await read();

  expect(bottom.content, 'the wheel moved the content area').toBeGreaterThan(0);
  expect(top.content, 'and brought it back to its top').toBe(0);
  for (const [at, reading] of [['top', before], ['bottom', bottom], ['back at top', top]] as const) {
    expect(reading.topbar, `the top bar's top, ${at}`).toBe(before.topbar);
    expect(reading.rail, `the rail's top, ${at}`).toBe(before.rail);
    expect(reading.document, `the document's scroll position, ${at}`).toBe(0);
    expect(reading.documentRoom, `what the document could scroll, ${at}`).toBeLessThanOrEqual(0);
  }
  expect(before.html, 'html overscroll-behavior').toBe('none');
  expect(before.body, 'body overscroll-behavior').toBe('none');
  expect(before.main, '#page overscroll-behavior').toBe('contain');
});
