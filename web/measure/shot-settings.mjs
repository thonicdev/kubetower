// Pictures of Settings > Extra, drawn by the harness's `settings-extra*` scenes
// (`main.tsx`, data in `settings.fixture.ts`).
//
//     node measure/shot-settings.mjs <out-dir> [scene ...]
//
// Uses the harness on 5847 when one is serving, and starts one otherwise.
import { spawn } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { chromium } from '@playwright/test';

const WEB = join(dirname(fileURLToPath(import.meta.url)), '..');
const PORT = process.env.KT_HARNESS_PORT ?? '5847';
const HARNESS = `http://127.0.0.1:${PORT}`;
const [out, ...asked] = process.argv.slice(2);
if (out === undefined) {
  console.error('usage: node measure/shot-settings.mjs <out-dir> [scene ...]');
  process.exit(2);
}
const scenes = asked.length > 0 ? asked : ['settings-extra', 'settings-extra-open', 'settings-extra-columns', 'console-pods-security-filtered'];

async function serving() {
  try {
    return (await fetch(HARNESS)).ok;
  } catch {
    return false;
  }
}
async function harness() {
  if (await serving()) return undefined;
  const child = spawn(join(WEB, 'node_modules/.bin/vite'), ['-c', 'measure/vite.config.ts', '--port', PORT, '--strictPort'], { cwd: WEB, stdio: ['ignore', 'ignore', 'inherit'] });
  for (let tries = 0; tries < 120; tries += 1) {
    if (await serving()) return child;
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  child.kill();
  throw new Error('the harness did not answer');
}

const server = await harness();
const browser = await chromium.launch();
try {
  mkdirSync(out, { recursive: true });
  for (const scene of scenes) {
    const wide = scene === 'console-pods-security-filtered';
    const context = await browser.newContext({ viewport: wide ? { width: 1760, height: 460 } : { width: 1280, height: 800 }, deviceScaleFactor: 1, reducedMotion: 'reduce', timezoneId: 'UTC', locale: 'en-US' });
    const page = await context.newPage();
    const list = scene === 'console-pods-security-filtered';
    // The list's picture: Security's column was ticked in Settings in this browser.
    if (list) await page.addInitScript(() => localStorage.setItem('kubetower.extracolumns', '{"security/cves":true}'));
    await page.goto(`${HARNESS}/?scene=${list ? 'console-pods-security' : scene}`);
    const ready = list ? 'text=2 critical' : '[data-extras] [data-extra]';
    // A cold harness re-optimises its dependencies on the first load and
    // reloads: one more try is the page after that.
    for (let tries = 0; ; tries += 1) {
      try {
        await page.waitForSelector(ready, { timeout: 20_000 });
        break;
      } catch (error) {
        if (tries === 2) throw error;
        await page.reload();
      }
    }
    if (scene.endsWith('-open')) {
      await page.click('[data-extra="kubecron"] [data-extra-edit]');
      await page.mouse.move(2, 798);
    }
    if (scene.endsWith('-columns')) {
      await page.click('[data-extra="pending"] [data-extra-edit]');
      await page.click('[data-extra="pending"] [data-extra-column] input');
      await page.mouse.move(2, 798);
    }
    if (list) {
      await page.click('[data-control="filter"]');
      await page.click('[data-filter-column="Security"] button');
      await page.locator('[data-filter-column="Security"] button').filter({ hasText: '2 critical' }).first().click();
      await page.keyboard.press('Escape');
      await page.mouse.move(2, 458);
      await page.waitForTimeout(400);
    }
    await page.evaluate(() => document.fonts.ready);
    await page.waitForTimeout(200);
    await page.screenshot({ path: join(out, `${scene}.png`), animations: 'disabled', caret: 'hide' });
    console.log(join(out, `${scene}.png`));
    await context.close();
  }
} finally {
  await browser.close();
  server?.kill();
}
