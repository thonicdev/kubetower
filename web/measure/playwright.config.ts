import { defineConfig, devices } from '@playwright/test';

/**
 * The layout path.
 *
 * **It starts its own server and needs nothing else** — that is the whole
 * difference from `web/playwright.config.ts`, which deliberately starts none
 * because it signs in to a console the operator is already running. This one
 * has no cluster, no password and no state: `measure/vite.config.ts` serves the
 * harness on 5847, and every answer the components would have asked a back end
 * for is fulfilled from the `measure/*.spec.ts` specs.
 *
 * `reuseExistingServer` is **false**, including locally. A harness that attaches
 * to whatever is already on 5847 measures whatever that is; the point of this
 * path is that the tree in front of it is what was measured.
 *
 * Chromium only, for the reason the browser path already gives: a second engine
 * doubles the wall clock to catch a class of defect this path is not looking
 * for.
 *
 * `retries: 0` on purpose. A geometry assertion that passes on the second
 * attempt is a flake, and a flaky layout check is the first thing anybody
 * switches off — so a retry would hide exactly the failure mode that kills this.
 */
export default defineConfig({
  testDir: '.',
  testMatch: /\.spec\.ts$/,
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 30_000,
  expect: { timeout: 5_000 },
  reporter: [['list']],
  use: {
    baseURL: 'http://127.0.0.1:5847',
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
    ...devices['Desktop Chrome'],
  },
  webServer: {
    command: 'vite -c measure/vite.config.ts',
    cwd: '..',
    url: 'http://127.0.0.1:5847/?scene=list',
    reuseExistingServer: false,
    timeout: 60_000,
    stdout: 'ignore',
    stderr: 'pipe',
  },
});
