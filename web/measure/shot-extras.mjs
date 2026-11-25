// The preview pictures of Settings > Extra: one screenshot of each shipped
// extra's main page, drawn by the layout harness's `extra-<key>` scenes
// (`main.tsx`, data in `extras.fixture.ts`), written to
// `app/extras/<key>/assets/preview.png`.
//
// To regenerate, from `web/`:
//
//     pnpm shots:extras                      (all of them)
//     pnpm shots:extras redis kubecron       (only those)
//
// It uses the harness on 5847 when one is already serving (`pnpm measure`),
// and otherwise starts one and stops it at the end.
//
// The pictures are meant to be the same from one run to the next, so a
// regenerated file shows up in a diff only when the page changed: the clock is
// frozen at one instant (the fixtures date everything from it), motion is
// reduced and animations are stopped, the viewport is fixed at 1280x800 at
// a device scale of 1 in UTC and en-US, and the picture is taken only once nothing on the page
// is still reading. A page that shows a failure, or asks for a route the
// fixture does not answer, stops the script instead of being photographed.
//
// It lives in `measure/` so that `@playwright/test` resolves from `web/`.
import { spawn } from 'node:child_process';
import { mkdirSync, statSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { chromium } from '@playwright/test';

const WEB = join(dirname(fileURLToPath(import.meta.url)), '..');
const HARNESS = 'http://127.0.0.1:5847';
const VIEWPORT = { width: 1280, height: 800 };
/** A fixed instant, a Wednesday morning: the fixtures measure every time back
 *  from it, and KubeCron's schedules agree with it. */
const CLOCK = new Date('2026-03-11T09:40:00Z');

/**
 * What says each page is drawn, beyond "nothing is reading": the part of the
 * page that arrives last. Redis's tiles say their engine only once probed, and
 * probing is the person's gesture, so the script presses Probe all.
 */
const EXTRAS = {
  argocd: { ready: ['[data-argo-project] [data-app]', '[data-argo-catalogue]'] },
  kubecron: { ready: ['section[aria-label="Heatmap"]', 'table tbody tr'] },
  kubeadjust: { ready: ['h1'] },
  runtime: { ready: ['[data-jvm-container]'] },
  netinspect: { ready: ['[data-matrix] [data-cell], [data-mini-graph]', '[data-verdict], [data-direction]'] },
  pending: { ready: ['[data-node-bars]'] },
  events: { ready: ['[data-timeline] [data-bar]', '[data-cause]'] },
  certmanager: { ready: ['[data-strip]', 'table tbody tr'] },
  dns: { ready: ['[data-dns] [data-tile]', '[data-bar]'] },
  redis: {
    ready: ['[data-deployment]'],
    async act(page) {
      await page.click('[data-probe-each]');
      await page.waitForSelector('[data-probe-each], [data-probe-stop]', { state: 'detached', timeout: 15_000 });
      // The pointer is left where the button was, over whatever took its
      // place: moved to the empty foot of the page, so no hover is drawn.
      await page.mouse.move(VIEWPORT.width - 2, VIEWPORT.height - 2);
      // The header is redrawn when Probe all goes, and a few of its pixels
      // came out a shade apart from one run to the next: one resize repaints
      // the whole page from scratch.
      await page.setViewportSize({ width: VIEWPORT.width, height: VIEWPORT.height + 1 });
      await page.setViewportSize(VIEWPORT);
    },
  },
  cnpg: { ready: ['[data-cluster-card]'] },
  security: { ready: ['[data-workload-row]', '[data-namespaces] [data-ns]'] },
  storage: { ready: ['[data-level-strip]', 'table tbody tr'] },
};

/**
 * Further pictures of an extra, beside its preview: a scene of
 * `extras.fixture.ts` and the file under `app/extras/<key>/assets/` it is
 * written to, taken when that extra is. Optional: what says that scene is
 * drawn (`ready`, the extra's own by default), a taller viewport (`height`),
 * and one element to photograph alone (`element`, a tab as its panel draws it).
 */
const MORE = {
  // The Certificate tab of an Ingress with two certificates, issuance chains drawn.
  certmanager: [{ scene: 'extra-certmanager-tab', file: 'chain.png', ready: ['[data-chain]'], element: '[data-shot]' }],
  // Every orphan by kind; the Storage tab of a claim, alone; the tile on the Overview.
  storage: [
    { scene: 'extra-storage-orphans', file: 'orphans.png', ready: ['[data-orphans] [data-volume]'] },
    { scene: 'extra-storage-claim-tab', file: 'claim-tab.png', ready: ['[data-storage-claim] [data-snapshot]'], element: '[data-shot]' },
    { scene: 'console-overview-storage', file: 'overview-tile.png', ready: ['[data-extra-tile="storage"]'], element: '[data-extra-tiles]' },
  ],
  // The Argo CD tab of an OutOfSync Deployment, alone.
  argocd: [{ scene: 'extra-argocd-tab', file: 'tab.png', ready: ['[data-argo-verdict]'], element: '[data-shot]' }],
  // One namespace's suggestions, with the commands opened.
  kubeadjust: [
    {
      scene: 'extra-kubeadjust-namespace',
      file: 'namespace.png',
      height: 1100,
      ready: ['[data-commands]'],
      async act(page) {
        await page.click('[data-commands] > summary');
        await page.mouse.move(VIEWPORT.width - 2, VIEWPORT.height - 2);
      },
    },
  ],
  // The tile on a Java pod's Details, and the JVM tab of that pod after
  // Inspect, each photographed alone.
  runtime: [
    // The page with the panel open on a pod, before Inspect: the button and its line.
    {
      scene: 'extra-runtime-panel',
      file: 'page-panel.png',
      ready: ['[data-jvm-container]'],
      async act(page) {
        await page.click('[data-jvm-container="shop/billing-7c9f4d-x2kq/app"] td');
        await page.waitForSelector('[data-jvm-panel] [data-jvm-inspect]', { timeout: 15_000 });
        await page.mouse.move(VIEWPORT.width - 2, VIEWPORT.height - 2);
      },
    },
    { scene: 'extra-runtime-tile', file: 'pod-tile.png', ready: ['[data-shortcut-meter]'], element: '[data-shot]' },
    {
      scene: 'extra-runtime-tab',
      file: 'pod-tab-inspected.png',
      ready: ['[data-jvm-inspect]'],
      element: '[data-shot]',
      async act(page) {
        await page.click('[data-jvm-inspect]');
        await page.waitForSelector('[data-jvm-result]', { timeout: 15_000 });
        await page.mouse.move(VIEWPORT.width - 2, VIEWPORT.height - 2);
      },
    },
    // The thread dump's section alone, after its button, with the blocked
    // worker-a's stack open under the deadlock.
    {
      scene: 'extra-runtime-threads',
      file: 'thread-dump.png',
      ready: ['[data-jvm-thread-dump]'],
      element: '[data-jvm-threads]',
      async act(page) {
        await page.click('[data-jvm-thread-dump]');
        await page.waitForSelector('[data-jvm-dump]', { timeout: 15_000 });
        await page.click('[data-jvm-thread="worker-a"] > button');
        await page.waitForSelector('[data-jvm-stack]');
        await page.mouse.move(VIEWPORT.width - 2, VIEWPORT.height - 2);
      },
    },
    // The class histogram's section alone, after its confirmation and Run.
    {
      scene: 'extra-runtime-histogram',
      file: 'class-histogram.png',
      ready: ['[data-jvm-histogram-start]'],
      element: '[data-jvm-histogram]',
      async act(page) {
        await page.click('[data-jvm-histogram-start]');
        await page.waitForSelector('[data-jvm-histogram-confirm]');
        await page.click('[data-jvm-histogram-run]');
        await page.waitForSelector('[data-jvm-histogram-result]', { timeout: 15_000 });
        await page.mouse.move(VIEWPORT.width - 2, VIEWPORT.height - 2);
      },
    },
    // The heap dump's confirmation, as it stands before anything runs.
    {
      scene: 'extra-runtime-threads',
      file: 'heap-dump-confirm.png',
      ready: ['[data-jvm-heap-dump]'],
      element: '[role="dialog"]',
      async act(page) {
        await page.click('[data-jvm-heap-dump]');
        await page.waitForSelector('[data-jvm-heap-confirm]', { timeout: 15_000 });
        await page.mouse.move(VIEWPORT.width - 2, VIEWPORT.height - 2);
      },
    },
    // The flight recorder's section alone: the durations, and its confirmation.
    {
      scene: 'extra-runtime-threads',
      file: 'flight-recorder.png',
      ready: ['[data-jvm-jfr]'],
      element: '[data-jvm-jfr]',
      async act(page) {
        await page.click('[data-jvm-jfr-seconds="60"]');
        await page.mouse.move(VIEWPORT.width - 2, VIEWPORT.height - 2);
      },
    },
    {
      scene: 'extra-runtime-threads',
      file: 'flight-recorder-confirm.png',
      ready: ['[data-jvm-jfr]'],
      element: '[role="dialog"]',
      async act(page) {
        await page.click('[data-jvm-jfr-seconds="60"]');
        await page.click('[data-jvm-jfr-record]');
        await page.waitForSelector('[data-jvm-jfr-confirm]', { timeout: 15_000 });
        await page.mouse.move(VIEWPORT.width - 2, VIEWPORT.height - 2);
      },
    },
    // The memory history's section alone, after its button.
    {
      scene: 'extra-runtime-history',
      file: 'memory-history.png',
      ready: ['[data-jvm-history-start]'],
      element: '[data-jvm-history]',
      async act(page) {
        await page.click('[data-jvm-history-start]');
        await page.waitForSelector('[data-jvm-history-result]', { timeout: 15_000 });
        await page.mouse.move(VIEWPORT.width - 2, VIEWPORT.height - 2);
      },
    },
  ],
  netinspect: [
    { scene: 'extra-netinspect-namespace', file: 'namespace.png', ready: ['[data-namespace-picture]', '[data-verdict]'], height: 1100 },
    { scene: 'extra-netinspect-service', file: 'service.png', ready: ['[data-backends]', '[data-policy-copy]'], height: 1500 },
    { scene: 'extra-netinspect-pod', file: 'pod.png' },
  ],
  // The Activity tab of a crash-looping pod, with the extra on, at a panel's width.
  events: [
    { scene: 'console-activity-on', file: 'activity.png', ready: ['[data-activity-view] [data-bar]', '[data-activity-view] [data-cause]', '[data-activity="change"]'], viewport: { width: 720, height: 900 } },
  ],
  // The pod view again at the width of a pod's panel, where its tab draws it.
  pending: [
    { scene: 'extra-pending-pod', file: 'pod.png' },
    { scene: 'extra-pending-pod', file: 'tab.png', viewport: { width: 720, height: 900 } },
    // The scheduler found victims: the nominated node, the priority class, the matrix mark.
    { scene: 'extra-pending-nominated', file: 'nominated.png', ready: ['[data-preemption="nominated"]'], viewport: { width: 1280, height: 640 } },
    // The Pending tab of a Deployment: its pods waiting, and a controller a quota refused.
    { scene: 'extra-pending-workload', file: 'workload-tab.png', ready: ['[data-pending-workload] [data-pending-pod]'], element: '[data-shot]' },
    { scene: 'extra-pending-refused', file: 'workload-refused.png', ready: ['[data-pending-workload] [data-quota]'], element: '[data-shot]' },
    // The Deployments list with the Pending column ticked in Settings > Extra.
    { scene: 'console-deployments-pending', file: 'column.png', ready: ['text=Cannot create'], viewport: { width: 1760, height: 420 }, columns: ['pending/reason'] },
    // The tile on a Pending pod's panel, alone; then the window it opens.
    { scene: 'extra-pending-tile', file: 'tile.png', ready: ['[data-shortcut]'], element: '[data-shot]' },
    {
      scene: 'extra-pending-tile',
      file: 'window.png',
      ready: ['[data-shortcut]'],
      viewport: { width: 1440, height: 1200 },
      element: '[role="dialog"]',
      async act(page) {
        await page.click('[data-shortcut] button');
        await page.waitForSelector('[data-pending-window] [data-pod-view]', { timeout: 15_000 });
        await page.mouse.move(VIEWPORT.width - 2, VIEWPORT.height - 2);
      },
    },
  ],
  cnpg: [
    { scene: 'extra-cnpg-cluster', file: 'cluster.png', ready: ['[data-members]', '[data-backup-timeline]'], height: 1180 },
    // The topology once Read lag is pressed: each edge carries the replay lag, and the table the three lags.
    {
      scene: 'extra-cnpg-cluster',
      file: 'lag.png',
      ready: ['[data-lag-read]'],
      viewport: { width: 1280, height: 1400 },
      element: '[aria-label="Topology"]',
      async act(page) {
        await page.click('[data-lag-read]');
        await page.waitForSelector('[data-lag="read"] [data-lag-replica]', { timeout: 15_000 });
        await page.mouse.move(2, 2);
      },
    },
    { scene: 'extra-cnpg-cluster', file: 'commands.png', ready: ['[data-commands] [data-command]'], viewport: { width: 1280, height: 1400 }, element: '[data-commands]' },
    { scene: 'extra-cnpg-tab', file: 'panel.png', ready: ['[data-cnpg-tab] [data-members]'], element: '[data-cnpg-tab]' },
    // The database, once Connect is pressed: the statistics, then a table's first rows.
    {
      scene: 'extra-cnpg-cluster',
      file: 'database.png',
      ready: ['[data-connect]'],
      viewport: { width: 1280, height: 1400 },
      element: '[data-stats]',
      async act(page) {
        await page.click('[data-connect]');
        await page.waitForSelector('[data-figure="Connections"]', { timeout: 15_000 });
      },
    },
    {
      scene: 'extra-cnpg-cluster',
      file: 'tables.png',
      ready: ['[data-connect]'],
      viewport: { width: 1280, height: 1400 },
      element: '[data-tables]',
      async act(page) {
        await page.click('[data-connect]');
        await page.click('[data-table="public.orders"]');
        await page.waitForSelector('[data-rows="public.orders"] tbody tr', { timeout: 15_000 });
        await page.mouse.move(2, 2);
      },
    },
  ],
  // The workload's page; then a Deployment's tab and tile, each alone, and
  // the console's own Pods list with the extra's column.
  security: [
    { scene: 'extra-security', file: 'cluster.png', ready: ['[data-finding]'], element: '[data-cluster-reports]' },
    { scene: 'extra-security-rbac', file: 'rbac.png', ready: ['[data-finding]'] },
    { scene: 'extra-security-policies', file: 'policies.png', ready: ['[data-policy-row]', '[data-resource-row]'] },
    { scene: 'extra-security-policy', file: 'policy.png', ready: ['[data-policy-result]'] },
    { scene: 'extra-security-compliance', file: 'compliance.png', ready: ['[data-control]'] },
    { scene: 'extra-security-workload', file: 'detail.png', ready: ['[data-containers]', '[data-cve-row]'] },
    // The SBOM tab of a workload; then the workloads with Signature ✗ pressed.
    { scene: 'extra-security-sbom', file: 'sbom.png', ready: ['[data-sbom-images]', '[data-component]'], height: 1000 },
    {
      scene: 'extra-security',
      file: 'signature.png',
      ready: ['[data-tile="unsigned"]', '[data-workload-row]'],
      viewport: { width: 1600, height: 720 },
      async act(page) {
        await page.click('[data-tile="unsigned"]');
        await page.waitForSelector('[data-tile="unsigned"][aria-pressed="true"]', { timeout: 15_000 });
        await page.mouse.move(2, 2);
      },
    },
    { scene: 'extra-security-tab', file: 'tab.png', ready: ['[data-tab-cves]'], element: '[data-shot]' },
    { scene: 'extra-security-tile', file: 'tile.png', ready: ['[data-shortcut]'], element: '[data-shot]' },
    // A cluster without trivy-operator: Install pressed, the console's plan open.
    {
      scene: 'extra-security-install',
      file: 'install.png',
      ready: ['[data-install="offer"]'],
      async act(page) {
        await page.click('[data-install="offer"]');
        await page.waitForSelector('[data-install-yaml]', { timeout: 15_000 });
        await page.mouse.move(VIEWPORT.width - 2, VIEWPORT.height - 2);
      },
    },
    // Wide enough that the column, drawn after the console's own, is in view;
    // the column ticked in Settings > Extra in this browser, as a person would.
    {
      scene: 'console-pods-security',
      file: 'column.png',
      ready: ['text=2 critical'],
      viewport: { width: 1760, height: 420 },
      columns: ['security/cves'],
    },
  ],
};

const asked = process.argv.slice(2);
for (const key of asked) {
  if (!(key in EXTRAS)) {
    console.error(`no extra ${key}; one of ${Object.keys(EXTRAS).join(', ')}`);
    process.exit(2);
  }
}
const keys = asked.length > 0 ? asked : Object.keys(EXTRAS);

async function serving() {
  try {
    return (await fetch(HARNESS)).ok;
  } catch {
    return false;
  }
}

/** The harness, started here when none is serving; `undefined` when one was. */
async function harness() {
  if (await serving()) return undefined;
  const child = spawn(join(WEB, 'node_modules/.bin/vite'), ['-c', 'measure/vite.config.ts'], {
    cwd: WEB,
    stdio: ['ignore', 'ignore', 'inherit'],
  });
  for (let tries = 0; tries < 120; tries += 1) {
    if (await serving()) return child;
    if (child.exitCode !== null) throw new Error(`the harness exited with ${String(child.exitCode)}`);
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  child.kill();
  throw new Error(`the harness did not answer on ${HARNESS}`);
}

const server = await harness();
const browser = await chromium.launch();
let failed = false;
try {
  const shots = keys.flatMap((key) => [
    { key, scene: `extra-${key}`, file: 'preview.png', act: EXTRAS[key].act },
    ...(MORE[key] ?? []).map((more) => ({ key, ...more })),
  ]);
  for (const shot of shots) {
    const { key, scene, file } = shot;
    const ready = shot.ready ?? EXTRAS[key].ready;
    const viewport = shot.viewport ?? (shot.height === undefined ? VIEWPORT : { width: VIEWPORT.width, height: shot.height });
    const context = await browser.newContext({ viewport, deviceScaleFactor: 1, reducedMotion: 'reduce', timezoneId: 'UTC', locale: 'en-US' });
    const page = await context.newPage();
    await page.clock.setFixedTime(CLOCK);
    // An extra's list column is hidden until ticked (Settings > Extra), kept
    // per browser: a shot of one ticks it before the page loads.
    if (shot.columns !== undefined) {
      const ticked = JSON.stringify(Object.fromEntries(shot.columns.map((c) => [c, true])));
      await page.addInitScript((value) => localStorage.setItem('kubetower.extracolumns', value), ticked);
    }
    await page.goto(`${HARNESS}/?scene=${scene}`);
    for (const selector of ready) await page.waitForSelector(selector, { timeout: 30_000 });
    if (shot.act !== undefined) await shot.act(page);
    await page.waitForFunction(
      () => document.querySelector('[aria-busy="true"]') === null && !/Reading…|Probing/.test(document.body.innerText),
      undefined,
      { timeout: 15_000 },
    );
    const wrong = await page.evaluate(() => {
      const alert = document.querySelector('[role="alert"]');
      if (alert !== null) return `a failure is drawn: ${alert.textContent ?? ''}`;
      const missing = /no fixture for \S+/.exec(document.body.innerText);
      return missing === null ? undefined : missing[0];
    });
    if (wrong !== undefined) {
      console.error(`${scene}: ${wrong}`);
      failed = true;
      await context.close();
      continue;
    }
    // Fonts, and a frame for anything laid out after them.
    await page.evaluate(() => document.fonts.ready);
    await page.waitForTimeout(200);
    const out = join(WEB, 'app/extras', key, 'assets', file);
    mkdirSync(dirname(out), { recursive: true });
    // A tab is photographed alone, as the panel draws it.
    if (shot.element === undefined) await page.screenshot({ path: out, animations: 'disabled', caret: 'hide' });
    else await page.locator(shot.element).screenshot({ path: out, animations: 'disabled', caret: 'hide' });
    console.log(`${out} ${String(Math.round(statSync(out).size / 1024))} KB`);
    await context.close();
  }
} finally {
  await browser.close();
  server?.kill();
}
if (failed) process.exit(1);
