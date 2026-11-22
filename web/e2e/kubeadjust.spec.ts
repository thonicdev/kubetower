import { test as base, expect, type Page } from '@playwright/test';

import { clusterPath } from '../ui/src/clusters/clusterPath';
import { MUTATION_HEADER, PASSWORD, sessionFile } from './session';

/**
 * KubeAdjust in a browser, against a real cluster: the
 * page loads, its views switch through the address, a filter narrows, and the
 * page offers nothing that writes.
 *
 * "Offers nothing that writes" is what a person can reach on the page: no
 * form and no write verb, asserted on every view.
 *
 * **It turns the extra on for the cluster and puts it back as it found it.**
 * The switch is the console's own state, not the cluster's, and every extra
 * is off until it is turned on; under the server profile the switch
 * needs the chart's values to name `kubeadjust` and the signed-in person to
 * hold `use` on it. Nothing it does reaches the cluster but reads.
 */

const CLUSTER = process.env.KT_E2E_CLUSTER ?? '';

const test = base.extend({
  storageState: async ({ storageState }, provide) => {
    await provide(sessionFile() ?? storageState);
  },
});

test.skip(PASSWORD === '', 'KT_E2E_PASSWORD is not set: this path needs a console to sign in to');

/** Nothing on the page writes: no form, and no control named for a write. */
async function offersNothingThatWrites(page: Page): Promise<void> {
  const root = page.locator('#kubeadjust');
  await expect(root.locator('form')).toHaveCount(0);
  for (const name of await root.getByRole('button').allTextContents()) {
    expect(name).not.toMatch(/apply|delete|patch|scale|restart|set resources/i);
  }
}

test('KubeAdjust loads, switches views, filters, and has no write control', async ({ page }) => {
  test.skip(CLUSTER === '', 'KT_E2E_CLUSTER is not set');

  const switchPath = `/api/c/${encodeURIComponent(CLUSTER)}/extensions/kubeadjust`;
  const listed = await page.request.get(`/api/c/${encodeURIComponent(CLUSTER)}/extensions`);
  expect(listed.ok()).toBe(true);
  const before = ((await listed.json()) as { items?: { key: string; enabled: boolean }[] }).items?.find((e) => e.key === 'kubeadjust');
  expect(before, 'the console does not list KubeAdjust among its extensions').toBeDefined();
  let turnedOn = false;
  if (before?.enabled !== true) {
    const on = await page.request.put(switchPath, { headers: { [MUTATION_HEADER]: '1' }, data: { enabled: true } });
    expect(on.status(), 'KubeAdjust is off and the console would not turn it on').toBe(204);
    turnedOn = true;
  }

  try {
    await page.goto(`${clusterPath(CLUSTER)}/x/kubeadjust`);
    const root = page.locator('#kubeadjust');
    await expect(page.getByRole('heading', { name: 'KubeAdjust', level: 1 })).toBeVisible({ timeout: 30_000 });
    // The four notes, whatever the cluster runs: one line of sources, each
    // sentence behind it.
    await expect(root.locator('[data-sources]')).toBeVisible({ timeout: 60_000 });
    for (const label of ['Usage', 'Node capacity', 'Disk', 'History']) {
      await expect(root.locator(`[data-note="${label}"]`)).toHaveCount(1);
    }
    await offersNothingThatWrites(page);
    const measured = (await root.locator('[data-unmeasured]').count()) === 0;

    // Nodes, through the tab, into the address.
    await root.getByRole('link', { name: 'Nodes' }).click();
    await expect(page).toHaveURL(/[?&]view=nodes/);
    await expect(root).toHaveAttribute('data-view', 'nodes', { timeout: 60_000 });
    if (measured) {
      await expect(root.locator('[data-node]').first()).toBeVisible();
      // A tile narrows and a second click restores.
      const all = await root.locator('[data-node]').count();
      const tile = root.locator('[data-tile]:not([disabled])').filter({ hasNotText: /^0/ }).first();
      if ((await tile.count()) > 0) {
        await tile.click();
        await expect(page).toHaveURL(/[?&]state=/);
        await tile.click();
        await expect(root.locator('[data-node]')).toHaveCount(all);
      }
    }
    await offersNothingThatWrites(page);

    // Back to the namespaces, and into one.
    await root.getByRole('link', { name: 'Namespaces' }).click();
    await expect(root).toHaveAttribute('data-view', 'overview', { timeout: 60_000 });
    const firstNamespace = root.locator('[data-namespace-card]').first();
    if (measured && (await firstNamespace.count()) > 0) {
      const name = (await firstNamespace.getAttribute('data-namespace-card')) ?? '';
      await firstNamespace.getByRole('link').first().click();
      await expect(page).toHaveURL(new RegExp(`[?&]ns=${name}`));
      await expect(root).toHaveAttribute('data-view', 'namespace', { timeout: 60_000 });
      // A filter, through the address, and back.
      const chip = root.locator('[data-category]').first();
      await chip.click();
      await expect(page).toHaveURL(/[?&]cat=/);
      await chip.click();
      await expect(page).not.toHaveURL(/[?&]cat=/);
      await offersNothingThatWrites(page);
    }
  } finally {
    if (turnedOn) {
      await page.request.put(switchPath, { headers: { [MUTATION_HEADER]: '1' }, data: { enabled: false } });
    }
  }
});
