import { expect, test } from '@playwright/test';

/* -------------------------------------------------------------------------- */
/* 26 — a list of 5000 pods is listed whole and drawn through a window        */
/* -------------------------------------------------------------------------- */

/**
 * Every pod is in the list (the header counts 5000, the scroll height is 5000
 * rows), the page holds a bounded number of rows, scrolling to the end is quick
 * and the last pod is then drawn, with the column widths unchanged by it.
 *
 * `CAPTURES` names a directory to leave two screenshots in, for the reader of a
 * pull request; the assertions do not depend on it.
 */
const CAPTURES = process.env['KT_CAPTURES'];

test('5000 pods: bounded rows, scroll to the end without a freeze, stable columns', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto('/?scene=console-pods-many');
  await page.locator('#panel tbody tr').first().waitFor();
  const widthsOf = (): Promise<number[]> =>
    page.evaluate(() => [...document.querySelectorAll<HTMLElement>('#panel thead th')].map((th) => th.getBoundingClientRect().width));
  const rows = (): Promise<number> => page.locator('#panel tbody tr:not([data-spacer])').count();

  const before = await widthsOf();
  expect(await rows(), 'rows drawn at the top').toBeLessThan(120);
  await expect(page.getByText('5000 listed')).toBeVisible();

  const middle = await page.evaluate(async () => {
    const panel = document.getElementById('panel') as HTMLElement;
    panel.scrollTop = (panel.scrollHeight - panel.clientHeight) / 2;
    await new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done)));
    return panel.scrollTop;
  });
  expect(middle).toBeGreaterThan(1000);
  expect(await rows(), 'rows drawn in the middle').toBeLessThan(120);
  if (CAPTURES !== undefined) await page.screenshot({ path: `${CAPTURES}/pods-5000-1280-middle.png` });

  const took = await page.evaluate(async () => {
    const panel = document.getElementById('panel') as HTMLElement;
    const started = performance.now();
    for (let at = 1; at <= 20; at += 1) {
      panel.scrollTop = ((panel.scrollHeight - panel.clientHeight) * at) / 20;
      await new Promise((done) => requestAnimationFrame(done));
    }
    return performance.now() - started;
  });
  console.log(`5000 pods: 20 scroll steps to the end took ${took.toFixed(0)} ms`);
  expect(took, '20 steps to the end').toBeLessThan(5000);
  expect(await rows(), 'rows drawn at the end').toBeLessThan(120);
  await expect(page.locator('#panel tbody tr').last()).toBeVisible();
  const last = await page.locator('#panel tbody tr:not([data-spacer])').last().textContent();
  console.log(`last row drawn: ${last ?? ''}`);

  const after = await widthsOf();
  expect(after, 'column widths do not move while scrolling').toEqual(before);

  await page.evaluate(() => {
    (document.getElementById('panel') as HTMLElement).scrollTop = 0;
  });
  await page.locator('#panel tbody tr').first().waitFor();
  if (CAPTURES !== undefined) await page.screenshot({ path: `${CAPTURES}/pods-5000-1280.png` });
});
