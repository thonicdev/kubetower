// Pictures of the 0.2.2 proposals (#121, #122), drawn by the harness's
// `proposal-*` scenes (`proposals.tsx`), at 1280 and 390 px, in the dark theme.
//
//     node measure/shot-proposals.mjs <out-dir> [scene ...]
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
  console.error('usage: node measure/shot-proposals.mjs <out-dir> [scene ...]');
  process.exit(2);
}
const scenes = asked.length > 0 ? asked : [
  'proposal-news-a', 'proposal-news-b', 'proposal-news-c',
  'proposal-updates-a-waiting', 'proposal-updates-a-password', 'proposal-updates-a-sheet',
  'proposal-updates-b-downloading', 'proposal-updates-b-sheet',
  'proposal-updates-c-onquit', 'proposal-updates-c-sheet',
];
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
      await page.goto(`${HARNESS}/?scene=${scene}`);
      const ready = scene.startsWith('proposal-news') ? '[role="dialog"] img, [role="dialog"] li' : '[data-proposal-ready]';
      for (let tries = 0; ; tries += 1) {
        try {
          await page.waitForSelector(ready, { timeout: 20_000, state: 'attached' });
          break;
        } catch (error) {
          if (tries === 2) throw error;
          await page.reload();
        }
      }
      await page.evaluate(() => document.fonts.ready);
      await page.waitForTimeout(600);
      const file = join(out, `${scene.replace('proposal-', '')}-${viewport.name}.png`);
      await page.screenshot({ path: file, animations: 'disabled', caret: 'hide', fullPage: !scene.startsWith('proposal-news') });
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      console.log(`${file} overflow=${overflow}`);
      await context.close();
    }
  }
} finally {
  await browser.close();
  server?.kill();
}
