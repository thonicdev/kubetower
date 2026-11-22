import { existsSync } from 'node:fs';
import { delimiter, join } from 'node:path';
import { test as base, expect, type Page } from '@playwright/test';

// The console's own spelling of a cluster in an address, so
// the smoke path arrives on the address the console itself links to rather than
// on one its router has to redirect.
import { clusterPath } from '../ui/src/clusters/clusterPath';
import { PASSWORD, sessionFile } from './session';

/**
 * The one browser path, against a real cluster.
 *
 * Everything else is a unit test, and jsdom lays nothing out — so the fit of a
 * terminal, a control that is behind another element, a panel that throws on
 * mount and a link that 404s are all invisible to the unit tests.
 *
 * **Narrow on purpose.** One path that opens the console, walks the surfaces an
 * operator touches in the first minute, and presses every control it finds that
 * is safe to press. It is not a second test suite: a defect that a unit test can
 * catch belongs in a unit test, where it costs milliseconds.
 *
 * ## What it will not do
 *
 * **It presses nothing that writes.** Every destructive or mutating control is
 * located and asserted on — that it exists, that it is enabled or disabled for
 * the stated reason — and then left alone. A browser test that deletes a pod on
 * the operator's own cluster is a test that gets deleted.
 *
 * **It does not type the password.** A trace records what is typed and
 * what is sent, so every test here starts from the session `global-setup.ts`
 * opened through the API, which no trace records. The sign-in form, the first
 * thing an operator meets, is proved in `signin.spec.ts`, with tracing off.
 */

const CLUSTER = process.env.KT_E2E_CLUSTER ?? '';

/**
 * Every test here starts from the session global setup opened. Read when the
 * context is made, in the worker, and not when the config is loaded: the path
 * is a fresh directory global setup makes, after the config has been read.
 */
const test = base.extend({
  storageState: async ({ storageState }, provide) => {
    await provide(sessionFile() ?? storageState);
  },
});

test.skip(PASSWORD === '', 'KT_E2E_PASSWORD is not set: this path needs a console to sign in to');

/**
 * Opens the console on the session global setup kept, and says so when the
 * console does not take it - rather than leaving the next locator to time out
 * on a sign-in form.
 */
async function signedIn(page: Page): Promise<void> {
  await page.goto('/');
  const menu = page.locator('#menu');
  const form = page.getByLabel(/password/i);
  await expect
    .poll(async () => ((await menu.count()) > 0 ? 'in' : (await form.count()) > 0 ? 'form' : 'waiting'), {
      timeout: 30_000,
    })
    .not.toBe('waiting');
  if ((await menu.count()) === 0) {
    throw new Error('the console showed its sign-in form: the session global setup opened was not accepted');
  }
}

/**
 * Whether `name` is on this process's PATH. The console under test runs on the
 * same machine as this path, so its PATH is the one that decides whether a
 * Claude terminal can start.
 */
function onPath(name: string): boolean {
  const extensions = process.platform === 'win32' ? (process.env.PATHEXT ?? '.EXE;.CMD').split(';') : [''];
  return (process.env.PATH ?? '')
    .split(delimiter)
    .some((dir) => dir !== '' && extensions.some((ext) => existsSync(join(dir, name + ext))));
}

test('a resource list opens, a row opens the drawer, and its tabs answer', async ({ page }) => {
  test.skip(CLUSTER === '', 'KT_E2E_CLUSTER is not set');
  await signedIn(page);

  await page.goto(`${clusterPath(CLUSTER)}/pods`);
  const rows = page.locator('tbody tr');
  await expect(rows.first()).toBeVisible({ timeout: 30_000 });
  // The kinds column is drawn inside a cluster. On the list of clusters it is
  // mounted and empty, which hides it, so signin.spec.ts can only assert it
  // mounted; this is where its absence would show. A fresh context has it shown.
  await expect(page.locator('#menu')).toBeVisible();

  await rows.first().click();
  const drawer = page.locator('[data-object-chrome="drawer"]');
  await expect(drawer).toBeVisible();

  // The strip the server declares. Details is ours and is there at once; the
  // rest arrive with the payload, so the wait is on one of *those* - reading the
  // strip before the object has been read is reading our own half of it.
  const tabs = drawer.getByRole('tab');
  await expect(tabs.first()).toHaveText('Details');
  await expect(drawer.getByRole('tab', { name: 'Activity' })).toBeVisible({ timeout: 30_000 });
  const labels = await tabs.allTextContents();
  expect(labels).toContain('Activity');
  expect(labels).toContain('Manifest');
  // And the charts are **not** a tab: a pod's measurements are drawn on the
  // panel that opens, above the facts.
  expect(labels).not.toContain('Metrics');
  await expect(drawer.getByRole('group', { name: 'Measurement' })).toBeVisible({ timeout: 30_000 });

  // Every tab is opened and has to draw *something* - a panel, or a notice
  // saying why not. A blank panel is the one outcome that is always a defect.
  for (const label of labels) {
    await drawer.getByRole('tab', { name: label }).click();
    const panel = drawer.locator('[role="tabpanel"]');
    await expect(panel).toBeVisible();
    await expect(panel).not.toBeEmpty();
  }
});

test('a secret shows its keys, and the masked one is held back until asked', async ({ page }) => {
  test.skip(CLUSTER === '', 'KT_E2E_CLUSTER is not set');
  await signedIn(page);

  await page.goto(`${clusterPath(CLUSTER)}/secrets`);
  const rows = page.locator('tbody tr');
  await expect(rows.first()).toBeVisible({ timeout: 30_000 });
  await rows.first().click();

  const drawer = page.locator('[data-object-chrome="drawer"]');
  // **A section of Details, not a tab**: the keys sit between what the object
  // says about itself and Related (ObjectDetail.tsx). The wait is the assertion
  // as much as the wait: a Secret with no Data section is the server not
  // declaring `data` for it, which is the half of this that is in Go.
  const panel = drawer.locator('[data-section="data"]');
  await expect(panel).toBeVisible({ timeout: 30_000 });
  await expect(drawer.getByRole('tab', { name: 'Data' })).toHaveCount(0);
  // Either it has keys, or it says it has none. Both are answers; a blank
  // section is not.
  await expect(panel).not.toBeEmpty();

  // **The row, not the panel.** A Secret has several masked keys and revealing
  // one leaves the others held back, so the panel's first "held back" after a
  // reveal is the *next* key's.
  //
  // **And the row by its position, not by its Show button**, which a reveal
  // takes away: after the click "the first row with a Show button" names the
  // next masked key, still held back.
  const keys = panel.locator('li');
  let masked = -1;
  for (let at = 0; at < (await keys.count()); at += 1) {
    if ((await keys.nth(at).getByRole('button', { name: 'Show' }).count()) > 0) {
      masked = at;
      break;
    }
  }
  // On kind the first Secret is kubeadm's bootstrap token, whose six keys are
  // all masked text, so a reveal that found nothing to reveal would be the case
  // passing on nothing. Elsewhere a first Secret with no masked text key is an
  // ordinary thing, and the reveal half is left out there.
  if (CLUSTER.startsWith('kind-')) {
    expect(masked, 'a Secret on kind with no key offering Show').toBeGreaterThanOrEqual(0);
  }
  if (masked >= 0) {
    const row = keys.nth(masked);
    await expect(row.getByText(/held back until you ask for it/i)).toBeVisible();
    await row.getByRole('button', { name: 'Show' }).click();
    // The reveal is the one door, and it either answers or says why. What it
    // must not do is leave this row looking the same.
    await expect(row.getByText(/held back until you ask for it/i)).toBeHidden({
      timeout: 15_000,
    });
  }

  // The write control is found and asserted on, and never pressed.
  const edit = panel.getByRole('button', { name: 'Edit' }).first();
  if ((await edit.count()) > 0) {
    await expect(edit).toBeVisible();
  }
});

test('the topbar controls do what they say', async ({ page }) => {
  test.skip(CLUSTER === '', 'KT_E2E_CLUSTER is not set');
  await signedIn(page);
  await page.goto(`${clusterPath(CLUSTER)}/pods`);
  await expect(page.locator('tbody tr').first()).toBeVisible({ timeout: 30_000 });

  // No Cluster link beside Settings: the KubeTower crumb is the way to every
  // cluster, and leads to the list.
  await expect(page.locator('#topbar').getByRole('link', { name: 'Cluster', exact: true })).toHaveCount(0);
  await page.locator('#topbar').getByRole('link', { name: 'KubeTower', exact: true }).click();
  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole('heading', { level: 1, name: 'Clusters' })).toBeVisible();

  // A link where the header draws no signed-in name, and the first entry of
  // the menu under that name where it does - one cluster, and a way to sign
  // back in.
  // Retried as one gesture: arriving on `/` from a cluster, the header can
  // draw the Settings link and then replace it with the menu once the session
  // has been read, and a click on the link then lands on nothing.
  const account = page.locator('#topbar button[aria-haspopup="menu"]');
  await expect(async () => {
    if ((await account.count()) > 0) {
      await account.click({ timeout: 2_000 });
      await page.getByRole('menuitem', { name: 'Settings' }).click({ timeout: 2_000 });
    } else {
      await page.locator('#topbar').getByRole('link', { name: 'Settings' }).click({ timeout: 2_000 });
    }
    await expect(page).toHaveURL(/\/settings/, { timeout: 2_000 });
  }).toPass({ timeout: 30_000 });
  // This binary carries the extras, and Extra draws a tile per extension with
  // its switch - every one off on a cluster nobody chose. Asserted on, never
  // pressed.
  await page.getByRole('link', { name: 'Extra', exact: true }).click();
  await expect(page).toHaveURL(/\/settings\/extra/);
  for (const key of ['argocd', 'kubecron', 'kubeadjust', 'redis', 'jvm']) {
    const toggle = page.locator(`[data-extra="${key}"] [role="switch"]`);
    await expect(toggle).toBeVisible({ timeout: 30_000 });
    await expect(toggle).toHaveAttribute('aria-checked', 'false');
    await expect(toggle).toBeEnabled();
  }
  // Argo CD's column in the lists is off until asked for. Its setting is
  // in the window its Edit button opens.
  await page.locator('[data-extra="argocd"] [data-extra-edit]').click();
  await expect(page.locator('[role="dialog"] [data-extra-setting="list-column"] input')).not.toBeChecked({ timeout: 10_000 });
});

test('the verbs are offered on an object, and none of them is pressed', async ({ page }) => {
  test.skip(CLUSTER === '', 'KT_E2E_CLUSTER is not set');
  await signedIn(page);

  await page.goto(`${clusterPath(CLUSTER)}/pods`);
  await expect(page.locator('tbody tr').first()).toBeVisible({ timeout: 30_000 });
  await page.locator('tbody tr').first().click();

  const drawer = page.locator('[data-object-chrome="drawer"]');
  const verbs = drawer.getByRole('group', { name: 'Actions' });
  await expect(verbs).toBeVisible({ timeout: 30_000 });

  // Delete is on every kind. Its presence is the assertion; pressing it is not
  // this test's business and never will be.
  const remove = verbs.getByRole('button', { name: 'Delete' });
  await expect(remove).toBeVisible();
  await expect(remove).toBeEnabled();

  // The glyph resolved. An icon that draws nothing is the failure mode the
  // sprite has, and it is silent - `<use>` against a missing symbol renders an
  // empty box with no error anywhere.
  expect(await remove.locator('svg use').count()).toBe(1);
});

test('ticking rows fills the bar with verbs, and opens no drawer', async ({ page }) => {
  test.skip(CLUSTER === '', 'KT_E2E_CLUSTER is not set');
  await signedIn(page);

  await page.goto(`${clusterPath(CLUSTER)}/pods`);
  await expect(page.locator('tbody tr').first()).toBeVisible({ timeout: 30_000 });

  await page.locator('tbody input[type="checkbox"]').first().check();
  const bar = page.locator('#bulk-bar');
  await expect(bar).toBeVisible();
  await expect(bar).toContainText('1 selected');
  await expect(bar.getByRole('button', { name: 'Delete' })).toBeVisible();

  // The defect this pins: a control inside a row must decline the row's click.
  // Otherwise building a selection of six opens and replaces the drawer six
  // times, and the sixth stays open over the list being worked in.
  await expect(page.locator('[data-object-chrome="drawer"]')).toHaveCount(0);

  await page.locator('tbody input[type="checkbox"]').nth(1).check();
  await expect(bar).toContainText('2 selected');
  await expect(page.locator('[data-object-chrome="drawer"]')).toHaveCount(0);
});

test('the ways into a terminal are offered, and only the safe one is opened', async ({ page }) => {
  test.skip(CLUSTER === '', 'KT_E2E_CLUSTER is not set');
  // The Claude terminal starts `claude` beside the console, and a CI runner has
  // none: the smoke job on kind skips this case, and its run
  // summary names it. Skipped rather than failed, because the button would open
  // a session that cannot start, which says nothing about the console.
  test.skip(!onPath('claude'), 'claude is not on the PATH of this machine, so the Claude terminal cannot start');
  await signedIn(page);

  await page.goto(`${clusterPath(CLUSTER)}/pods`);
  await expect(page.locator('tbody tr').first()).toBeVisible({ timeout: 30_000 });
  await page.locator('tbody tr').first().click();

  const drawer = page.locator('[data-object-chrome="drawer"]');
  const terminals = drawer.getByRole('group', { name: 'Terminals' });
  await expect(terminals).toBeVisible({ timeout: 30_000 });

  // Both are drawn on a pod in the desktop profile. Their glyphs resolved, which
  // a unit test cannot see: `<use>` against a missing symbol draws an empty box
  // and reports nothing.
  const shell = terminals.getByRole('button', { name: 'Shell' });
  await expect(shell).toBeVisible();
  expect(await shell.locator('svg use').count()).toBe(1);
  await expect(terminals.getByRole('button', { name: /Claude/ })).toBeVisible();

  // **Claude is the one that is safe to open**: it starts a session on this
  // machine, against a kubeconfig narrowed to one cluster, and closing the tab
  // ends it. A pod shell would be a root-equivalent prompt inside somebody's
  // workload, and the node shell creates a pod on a node - neither belongs in a
  // test that runs unattended.
  await terminals.getByRole('button', { name: /Claude/ }).click();

  const dock = page.locator('#dock');
  await expect(dock).toBeVisible();
  // The label says which resource the session is about.
  await expect(dock).toContainText('Claude');

  // And it drew a terminal rather than an empty panel - the thing jsdom cannot
  // establish, because it lays nothing out.
  await expect(dock.locator('canvas, .xterm').first()).toBeVisible({ timeout: 20_000 });

  // The one thing jsdom cannot do: the tab comes back after a reload - the
  // same kind on the same object - and it is live: a fresh session, not the one
  // that died with the socket.
  await page.reload();
  await expect(page.locator('#menu')).toBeVisible({ timeout: 30_000 });
  await expect(dock).toContainText('Claude', { timeout: 20_000 });
  await expect(dock.locator('canvas, .xterm').first()).toBeVisible({ timeout: 20_000 });
});

test('a relation leads to its own list, with the object open in the drawer', async ({ page }) => {
  test.skip(CLUSTER === '', 'KT_E2E_CLUSTER is not set');
  await signedIn(page);

  await page.goto(`${clusterPath(CLUSTER)}/pods`);
  await expect(page.locator('tbody tr').first()).toBeVisible({ timeout: 30_000 });
  const before = page.url();

  // The first pod that has a relation to follow. Not every pod does - a bare one
  // with no ConfigMap, Secret or Service is an ordinary thing - so it walks
  // until it finds one rather than asserting on the first. Twenty is the
  // ceiling because a cluster where twenty consecutive pods mount nothing is a
  // cluster this path has nothing to say about.
  const drawer = page.locator('[data-object-chrome="drawer"]');
  const rows = page.locator('tbody tr');
  let link = null;
  for (let at = 0; at < Math.min(await rows.count(), 20); at += 1) {
    await rows.nth(at).click();
    await expect(drawer).toBeVisible();
    // `*=` and not `^=`: the rendered href carries the router's basename, so a
    // relation reads `/c/…` and a match anchored at `/c/` finds nothing.
    // Not a selector's link (`data-selects`), which opens a filtered list and no
    // object.
    const candidate = drawer.locator('[data-section="relations"] a[href*="/c/"]:not([data-selects])').first();
    if ((await candidate.count()) > 0) {
      link = candidate;
      break;
    }
  }
  test.skip(link === null, 'no pod in this cluster has a linkable relation');

  const href = await link!.getAttribute('href');
  const target = new URL(href ?? '', page.url());
  const opened = target.searchParams.get('open') ?? '';
  await link!.click();

  // **A navigation, to the related kind's list, with the object open on
  // arrival** (Relations.tsx): click the ReplicaSet under a pod and be on the
  // ReplicaSets page, narrowed to its namespace, with that ReplicaSet in the
  // drawer, because the list gives the context the drawer alone does not.
  expect(opened).not.toBe('');
  await expect(page).toHaveURL((url) => url.pathname === target.pathname);
  expect(page.url()).not.toBe(before);
  await expect(page.locator('tbody tr').first()).toBeVisible({ timeout: 30_000 });
  await expect(drawer).toBeVisible({ timeout: 30_000 });
  await expect(drawer.getByRole('heading', { level: 2 })).toHaveText(opened.split('/').pop() ?? '');

  // And the anchor is still an anchor, which is what a middle-click and a paste
  // need.
  expect(href).toContain('/c/');
});
