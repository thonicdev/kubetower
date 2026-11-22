import { expect, test, type Page } from '@playwright/test';
import { PASSWORD, USER } from './session';

/**
 * The sign-in form, the first thing an operator meets, and the one test of the
 * browser path that types the password.
 *
 * **In a file of its own, with tracing off.** A Playwright trace records every
 * action with its arguments and every request with its body, so a failing run
 * of this test would keep the password in clear in its `trace.zip`, and the CI
 * job uploads `test-results/` when it fails. Tracing cannot
 * be turned off for one group inside a file - Playwright refuses, because it
 * forces a new worker - so this test has the file.
 *
 * **And it leaves no form behind when it fails.** Tracing off is not enough:
 * Playwright writes `error-context.md` for every failed test, whatever `trace`
 * says, with an accessibility snapshot of the page, and a filled password field
 * is in that snapshot in clear (`- textbox "Password": <the word>`). An `expect`
 * that fails takes the snapshot at that moment, with the form still filled, so
 * `signIn` waits with `waitFor`, which takes none, and on any failure leaves for
 * `about:blank` before it throws: the snapshot is then of an empty page. The
 * global teardown refuses a run that left the password in `test-results/`
 * anyway (global-teardown.ts).
 *
 * It starts signed out, so it is the form that lets it in and not the session
 * `global-setup.ts` opened for every other test.
 */

test.use({ storageState: { cookies: [], origins: [] }, trace: 'off' });

test.skip(PASSWORD === '', 'KT_E2E_PASSWORD is not set: this path needs a console to sign in to');

/**
 * Signs in through the form. The only place in the browser path that types the
 * password; see the top of this file for why it is here.
 */
async function signIn(page: Page): Promise<void> {
  let failure: string | undefined;
  try {
    await page.goto('/');
    // The user field is not optional: left empty, the form submits, the server
    // refuses, and the test times out waiting for a menu with the reason on screen.
    await page.getByLabel(/^user$/i).fill(USER);
    await page.getByLabel(/password/i).fill(PASSWORD);
    await page.getByRole('button', { name: /sign in|log in/i }).click();

    // Wait for either outcome and say which. A refusal has to fail *as a
    // refusal*, with the console's own sentence, or the next reader spends an
    // afternoon on the wrong half. `attached`, because on the list of clusters
    // the kinds column is mounted and empty, which hides it (Menu.tsx).
    const menu = page.locator('#menu');
    const refusal = page.getByRole('alert');
    await menu.or(refusal).first().waitFor({ state: 'attached', timeout: 30_000 });
    if ((await menu.count()) === 0) {
      failure = `sign-in refused: ${(await refusal.first().textContent()) ?? 'no reason given'}`;
    }
  } catch (error) {
    // A Playwright error names the locator and the timeout. Its call log can
    // describe the element it resolved to, so the word is cut out regardless.
    const said = error instanceof Error ? error.message : String(error);
    failure = `sign-in did not complete: ${said.replaceAll(PASSWORD, '<the password>')}`;
  }
  if (failure !== undefined) {
    await page.goto('about:blank');
    throw new Error(failure);
  }
}

test('the console opens, and every region is drawn', async ({ page }) => {
  const broken: string[] = [];
  page.on('pageerror', (error) => broken.push(`pageerror: ${error.message}`));
  page.on('console', (message) => {
    if (message.type() === 'error') broken.push(`console: ${message.text()}`);
  });

  await signIn(page);

  // The four regions the Shell mounts. A missing one is not a styling problem:
  // it is the layout route having unmounted, which no unit test can see.
  //
  // **Mounted, not drawn, for the kinds column.** Signing in lands on the list
  // of clusters, where no cluster is chosen and `#menu` has no kind to list, so
  // it hides itself (`empty:hidden`, Menu.tsx), so asserting it visible here
  // fails on a fresh state directory. That it is drawn inside a cluster is
  // asserted by smoke.spec.ts's list case.
  await expect(page.locator('#menu')).toBeAttached();
  await expect(page.locator('#page')).toBeVisible();

  // The sheet the icons resolve against. `<use href="#i-...">` draws nothing at
  // all when it is absent, silently, which is exactly the failure a screenshot
  // catches and a test of the component does not.
  expect(await page.locator('symbol#i-trash').count()).toBe(1);

  expect(broken).toEqual([]);
});
