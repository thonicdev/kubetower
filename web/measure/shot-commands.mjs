// Captures of the pod panel's container tiles, on the
// `commands` scene, at the drawer's width (1600px viewport, so `--drawer-w` is
// its 560px cap), every script opened so the whole of it is in the picture -
// or left as the tile first draws it, folded, with `closed` after the path.
//
//     pnpm measure                      (in another terminal, serves 5847)
//     node measure/shot-commands.mjs <out.png> [closed]
import { chromium } from '@playwright/test';

const out = process.argv[2];
if (out === undefined) {
  console.error('usage: node measure/shot-commands.mjs <out.png>');
  process.exit(2);
}

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1600, height: 900 }, deviceScaleFactor: 1 });
await page.goto('http://127.0.0.1:5847/?scene=commands');
await page.waitForSelector('[data-pod-panel] [data-container]', { timeout: 15_000 });
if (process.argv[3] !== 'closed') {
  for (const summary of await page.locator('[data-command-script]:not([open]) > summary').all()) {
    await summary.click();
  }
  const closed = page.locator('[data-pod-panel] [data-fold-control][aria-expanded="false"]');
  while ((await closed.count()) > 0) await closed.first().click();
}
const height = await page.evaluate(() => {
  const panel = document.querySelector('[data-pod-panel]');
  if (panel === null) throw new Error('the pod panel is missing');
  return Math.ceil(panel.getBoundingClientRect().bottom + 1);
});
await page.setViewportSize({ width: 1600, height: Math.max(height, 300) });
await page.waitForTimeout(200);
await page.locator('[data-pod-panel]').screenshot({ path: out });
await browser.close();
console.log(out);
