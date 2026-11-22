import { defineConfig, devices } from '@playwright/test';

/**
 * The browser path.
 *
 * **It starts no server.** `webServer` is deliberately absent: this path signs
 * in to a console that is already running against a real cluster, and the one
 * thing it must never do is start one on the port a person is using. Point
 * it with `KT_E2E_URL`, give it `KT_E2E_PASSWORD`, and it skips itself entirely
 * when either is missing — so `pnpm e2e` on a machine with no console is a pass
 * with skips rather than a wall of failures nobody can act on.
 *
 * Chromium only. A second engine doubles the wall clock to catch a class of
 * defect this path is not looking for; the defects it *is* looking for — a
 * control behind another element, a panel that throws on mount, an icon sheet
 * that did not load — are the same in all three.
 *
 * `testDir` is its own folder rather than beside the units: Vitest owns
 * `ui/src/**` and would otherwise try to run these, which fails in a way that
 * reads as a broken test rather than a misrouted one.
 *
 * **The password is in no trace.** `trace: 'retain-on-failure'` records
 * every action with its arguments and every request with its body, so a test
 * that typed the password and failed would leave it in clear in a
 * `trace.zip` under `test-results/`. The run signs in once, through the API,
 * in `e2e/global-setup.ts`, which no trace records, and every test of
 * smoke.spec.ts starts from that session (`storageState`). The one test that
 * still types it into the form, to prove the form, turns tracing off for itself
 * and leaves no filled form behind when it fails (signin.spec.ts); the global
 * teardown refuses a run that left the password in `test-results/` anyway.
 *
 * **`KT_E2E_TRACE=off` records no trace at all.** A trace of a signed-in test
 * holds the session cookie in clear (e2e/session.ts), so the CI job, which
 * uploads `test-results/` on a failure, sets it. Screenshots and
 * `error-context.md` are still written.
 */
export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  workers: 1,
  timeout: 60_000,
  expect: { timeout: 10_000 },
  reporter: [['list']],
  globalSetup: './e2e/global-setup.ts',
  globalTeardown: './e2e/global-teardown.ts',
  use: {
    baseURL: process.env.KT_E2E_URL ?? 'http://127.0.0.1:5823',
    // A failure that cannot be looked at is a failure somebody argues with.
    screenshot: 'only-on-failure',
    trace: process.env.KT_E2E_TRACE === 'off' ? 'off' : 'retain-on-failure',
    ...devices['Desktop Chrome'],
  },
});
