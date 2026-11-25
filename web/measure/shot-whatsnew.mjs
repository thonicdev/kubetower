// Pictures of Welcome and What's new (#107), drawn by the harness's
// `console-whats-new-*` scenes (`main.tsx`, data in `whatsnew.fixture.ts`),
// at 1280 and 390 px, in the dark theme.
//
//     node measure/shot-whatsnew.mjs <out-dir> [scene ...]
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
  console.error('usage: node measure/shot-whatsnew.mjs <out-dir> [scene ...]');
  process.exit(2);
}
const scenes = asked.length > 0 ? asked : ['console-whats-new-welcome', 'console-whats-new-news', 'console-whats-new-tour'];
const VIEWPORTS = [
  { name: '1280', width: 1280, height: 800 },
  { name: '390', width: 390, height: 844 },
];

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
    for (const viewport of VIEWPORTS) {
      const context = await browser.newContext({
        viewport: { width: viewport.width, height: viewport.height },
        deviceScaleFactor: 1,
        colorScheme: 'dark',
        reducedMotion: 'reduce',
        timezoneId: 'UTC',
        locale: 'en-US',
      });
      const page = await context.newPage();
      // The console draws no dialog under automation; this picture is of what a person sees.
      await page.addInitScript(() => Object.defineProperty(Navigator.prototype, 'webdriver', { get: () => false }));
      await page.goto(`${HARNESS}/?scene=${scene}`);
      for (let tries = 0; ; tries += 1) {
        try {
          await page.waitForSelector('[role="dialog"] [data-whats-new]', { timeout: 20_000 });
          break;
        } catch (error) {
          if (tries === 2) throw error;
          await page.reload();
        }
      }
      await page.evaluate(() => document.fonts.ready);
      await page.waitForTimeout(400);
      const file = join(out, `${scene.replace('console-whats-new-', '')}-${viewport.name}.png`);
      await page.screenshot({ path: file, animations: 'disabled', caret: 'hide' });
      // Nothing wider than the window: no horizontal scroll at 390 px.
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      console.log(`${file} overflow=${overflow}`);
      await context.close();
    }
  }
} finally {
  await browser.close();
  server?.kill();
}
