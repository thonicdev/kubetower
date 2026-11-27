import { expect, test, type Page } from '@playwright/test';
import { show } from './layoutkit';

/* -------------------------------------------------------------------------- */
/* A debug container from the drawer (#147)                                   */
/* -------------------------------------------------------------------------- */

/**
 * `?scene=console-debug-before|after|shell` (`pods.fixture.ts`'s
 * `debugAnswers`): a pod whose app is distroless, the same pod once a debug
 * container joined it, and the same pod with nginx in its app. At 1280 and
 * 390px:
 *
 * - the page never scrolls sideways, popover open or not;
 * - Debug is beside Shell for the distroless pod, and absent for nginx;
 * - the debug form, opened from Debug or from the Shell menu, is wholly inside
 *   the window;
 * - the ephemeral tile shows its `stays in pod` badge, inside the tile.
 */

const SHOTS = process.env['DEBUG_SHOTS'];

async function sideways(page: Page): Promise<number> {
  return page.evaluate(() => {
    const doc = document.scrollingElement as HTMLElement;
    return doc.scrollWidth - doc.clientWidth;
  });
}

async function formInside(page: Page): Promise<{ left: number; right: number; top: number; bottom: number; width: number; height: number }> {
  const form = page.locator('[data-debug-form]');
  await expect(form).toBeVisible();
  const box = await form.boundingBox();
  if (box === null) throw new Error('the debug form has no box');
  const view = page.viewportSize();
  if (view === null) throw new Error('no viewport');
  return {
    left: box.x,
    right: view.width - (box.x + box.width),
    top: box.y,
    bottom: view.height - (box.y + box.height),
    width: box.width,
    height: box.height,
  };
}

for (const width of [1280, 390]) {
  const at = `${String(width)}px`;

  test(`Debug sits beside Shell for a distroless pod, and its form stays in the window, at ${at}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await show(page, 'console-debug-before');
    const debug = page.locator('[aria-label="Terminals"] button[data-debug-entry]');
    await expect(debug).toBeVisible();
    await expect(page.locator('[aria-label="Terminals"] button[aria-label="Shell menu"]')).toBeVisible();
    expect(await sideways(page), `${at}: the page scrolls sideways`).toBeLessThanOrEqual(0);

    await debug.click();
    const box = await formInside(page);
    for (const [edge, room] of Object.entries(box).filter(([key]) => key !== 'width' && key !== 'height')) {
      expect(room, `${at}: the debug form ends ${String(-room)}px past the window's ${edge}`).toBeGreaterThanOrEqual(0);
    }
    expect(await sideways(page), `${at}: the page scrolls sideways with the form open`).toBeLessThanOrEqual(0);
    if (SHOTS !== undefined) await page.screenshot({ path: `${SHOTS}/${String(width)}-debug-button.png` });
  });

  test(`no Debug beside Shell for an nginx pod, and the Shell menu carries the form inside the window, at ${at}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await show(page, 'console-debug-shell');
    const menu = page.locator('[aria-label="Terminals"] button[aria-label="Shell menu"]');
    await expect(menu).toBeVisible();
    await expect(page.locator('[aria-label="Terminals"] button[data-debug-entry]')).toHaveCount(0);

    await menu.click();
    await expect(page.locator('[data-debug-in-menu]')).toBeVisible();
    const box = await formInside(page);
    for (const [edge, room] of Object.entries(box).filter(([key]) => key !== 'width' && key !== 'height')) {
      expect(room, `${at}: the menu's debug form ends ${String(-room)}px past the window's ${edge}`).toBeGreaterThanOrEqual(0);
    }
    expect(await sideways(page), `${at}: the page scrolls sideways with the menu open`).toBeLessThanOrEqual(0);
    if (SHOTS !== undefined) await page.screenshot({ path: `${SHOTS}/${String(width)}-shell-menu.png` });
  });

  test(`the debug container's tile shows that it stays in the pod, at ${at}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1200 });
    await show(page, 'console-debug-after');
    const tile = page.locator('article[data-ephemeral="true"]');
    await expect(tile).toHaveCount(1);
    const kept = tile.locator('[data-kept]');
    await expect(kept).toBeVisible();
    await expect(page.locator('[data-ephemeral-count]')).toHaveText('1 debug');

    const fits = await tile.evaluate((article) => {
      const box = article.getBoundingClientRect();
      const badge = article.querySelector('[data-kept]')?.getBoundingClientRect();
      if (badge === undefined) throw new Error('no kept badge');
      return { past: badge.right - box.right, sideways: article.scrollWidth - article.clientWidth };
    });
    expect(fits.past, `${at}: the kept badge ends ${String(fits.past)}px past its tile`).toBeLessThanOrEqual(0.5);
    expect(fits.sideways, `${at}: the tile scrolls sideways`).toBeLessThanOrEqual(0);
    expect(await sideways(page), `${at}: the page scrolls sideways`).toBeLessThanOrEqual(0);
    if (SHOTS !== undefined) await page.screenshot({ path: `${SHOTS}/${String(width)}-after.png` });
  });
}
