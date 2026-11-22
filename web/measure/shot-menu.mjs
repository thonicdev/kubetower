// The kinds column, photographed alone on the `menu-kinds*` scenes: Custom
// Resources opened, and its first two API groups.
//
//     pnpm measure                      (in another terminal, serves 5847)
//     node measure/shot-menu.mjs <scene> <out.png> [favourite-slug ...]
import { chromium } from '@playwright/test';

const [scene, out, ...favorites] = process.argv.slice(2);
if (scene === undefined || out === undefined) {
  console.error('usage: node measure/shot-menu.mjs <scene> <out.png>');
  process.exit(2);
}
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1280, height: 900 }, deviceScaleFactor: 1, reducedMotion: 'reduce' });
if (favorites.length > 0) {
  // Marked in this browser, as the column keeps them where the server does not.
  await page.addInitScript((slugs) => {
    localStorage.setItem('kubetower.kinds', JSON.stringify({ showUnused: true, favorites: { 'prod-eu-west': slugs } }));
  }, favorites);
}
await page.goto(`http://127.0.0.1:5847/?scene=${scene}`);
await page.waitForSelector('#menu details[data-group="workloads"]', { timeout: 30_000 });
await page.click('#menu details[data-group="custom-resources"] > summary');
await page.waitForSelector('#menu details[data-group="cert-manager.io"]', { timeout: 15_000 });
const groups = page.locator('#menu details[data-group="custom-resources"] details > summary');
await groups.nth(1).click();
await page.mouse.move(1270, 890);
await page.evaluate(() => document.fonts.ready);
await page.waitForTimeout(300);
await page.locator('#menu').screenshot({ path: out });
await browser.close();
console.log(out);
