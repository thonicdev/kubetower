// Captures of the rules panel, on the `rbac` scene, one per fixture case and
// width, and a check that nothing scrolls sideways at 1280, 1024 and 390px.
//
//     pnpm measure                      (in another terminal, serves 5847)
//     node measure/shot-rbac.mjs <out-dir> <before|after>
import { chromium } from '@playwright/test';

const [dir, tag] = [process.argv[2], process.argv[3]];
if (dir === undefined || tag === undefined) {
  console.error('usage: node measure/shot-rbac.mjs <out-dir> <before|after>');
  process.exit(2);
}

const cases = ['short', 'cluster-admin', 'long', 'aggregated', 'role'];
const widths = [1280, 1024, 390];
const browser = await chromium.launch();
let wide = 0;
for (const width of widths) {
  const page = await browser.newPage({ viewport: { width, height: 900 }, deviceScaleFactor: 1 });
  for (const name of cases) {
    await page.goto(`http://127.0.0.1:5847/?scene=rbac&case=${name}`);
    await page.waitForSelector('[data-rbac-panel] [data-section="rules"]', { timeout: 15_000 });
    await page.waitForTimeout(150);
    const m = await page.evaluate(() => {
      const root = document.documentElement;
      const panel = document.querySelector('[data-rbac-panel]');
      const sideways = [panel, ...panel.querySelectorAll('*')].filter((el) => el.scrollWidth > el.clientWidth + 1 && el.clientWidth > 0).length;
      return { doc: root.scrollWidth <= root.clientWidth, panel: panel.scrollWidth <= panel.clientWidth, sideways };
    });
    const ok = m.doc && m.panel;
    if (!ok) wide += 1;
    console.log(`${name}-${width}-${tag}: doc ${m.doc ? 'fits' : 'SCROLLS'}, panel ${m.panel ? 'fits' : 'SCROLLS'}, overflowing elements ${m.sideways}`);
    if (width !== 1024) {
      await page.locator('[data-rbac-panel]').screenshot({ path: `${dir}/${name}-${width}-${tag}.png` });
    }
  }
  await page.close();
}
await browser.close();
process.exit(wide === 0 ? 0 : 1);
