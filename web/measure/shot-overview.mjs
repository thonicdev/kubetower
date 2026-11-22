// A design capture: one full-page screenshot of the console scene at 1440px.
//
//     pnpm measure                      (in another terminal, serves 5847)
//     node measure/shot-overview.mjs <out.png>
//
// The viewport is 1440 wide because the entry names 1440, and as tall as the
// dashboard: the page scrolls inside `#page` and not in the document, so
// Playwright's `fullPage` sees one screen — measured, 1440x900 — and the whole
// page's hierarchy is what the pair is about. So the panel's `scrollHeight` is
// read once it is drawn and the viewport is grown to it. The browser is the
// harness's own Chromium, and the wait is on the last panel's table rather than
// on a timer: the page is done when Warnings has rows, and not before.
import { chromium } from '@playwright/test';

const out = process.argv[2];
if (out === undefined) {
  console.error('usage: node measure/shot-overview.mjs <out.png>');
  process.exit(2);
}

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1 });
await page.goto('http://127.0.0.1:5847/?scene=overview');
await page.waitForSelector('table[aria-label="Recent warning events"] tbody tr', { timeout: 15_000 });
const height = await page.evaluate(() => {
  const panel = document.getElementById('page');
  if (panel === null) throw new Error('#page is missing');
  return Math.ceil(panel.scrollHeight + panel.getBoundingClientRect().top + 1);
});
await page.setViewportSize({ width: 1440, height });
// One frame more, for the sparklines' and the rings' paint at the new size.
await page.waitForTimeout(300);
await page.screenshot({ path: out });
await browser.close();
console.log(out);
