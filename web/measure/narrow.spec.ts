import { expect, test, type Page } from '@playwright/test';
import { MANIFEST_TIMESTAMP } from './pods.fixture';
import { px, show } from './layoutkit';

/* -------------------------------------------------------------------------- */
/* The console at narrow widths: a phone at 390, a small window at 900        */
/* -------------------------------------------------------------------------- */

/** The drawer's box, the box it shares with the list, and the window. */
async function drawerBox(page: Page): Promise<{ left: number; right: number; width: number; shared: { left: number; right: number }; position: string }> {
  return page.evaluate(() => {
    const drawer = document.getElementById('drawer');
    const shared = drawer?.parentElement;
    if (drawer === null || shared === null || shared === undefined) throw new Error('no drawer');
    const box = drawer.getBoundingClientRect();
    const around = shared.getBoundingClientRect();
    return {
      left: box.left,
      right: box.right,
      width: box.width,
      shared: { left: around.left, right: around.right },
      position: getComputedStyle(drawer).position,
    };
  });
}

/** Every element under `selector` whose text is drawn shorter than it is. */
async function cut(page: Page, selector: string): Promise<string[]> {
  return page.evaluate((within) => {
    const out: string[] = [];
    for (const one of document.querySelectorAll<HTMLElement>(within)) {
      if (one.scrollWidth > one.clientWidth + 1) out.push(`${one.textContent?.trim() ?? ''} (${String(one.scrollWidth)} in ${String(one.clientWidth)})`);
    }
    return out;
  }, selector);
}

for (const kind of ['pods', 'secrets'] as const) {
  test(`at 390 the ${kind} drawer is a sheet over the whole content area, its tabs whole`, async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 800 });
    await show(page, `console-drawer-${kind}`);
    await page.locator('#drawer:not([hidden]) [role="tab"]').first().waitFor();

    const box = await drawerBox(page);
    console.log(`390, ${kind}: drawer ${px(box.left)} to ${px(box.right)}, content area ${px(box.shared.left)} to ${px(box.shared.right)}`);
    expect(box.left, 'the drawer does not start where the content area does').toBeCloseTo(box.shared.left, 0);
    expect(box.right, 'the drawer does not reach the window edge').toBeCloseTo(box.shared.right, 0);
    expect(box.right).toBeCloseTo(390, 0);

    const tabs = await cut(page, '#drawer [role="tab"]');
    expect(tabs, 'a drawer tab is cut').toEqual([]);
    await expect(page.locator('#drawer button[aria-label="Close"]')).toBeVisible();
    // Nothing to resize against on a phone.
    await expect(page.getByRole('separator', { name: 'Resize the panel' })).toBeHidden();
  });
}

test('at 1440 the drawer still sits beside the list at its default width', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await show(page, 'console-drawer-secrets');
  await page.locator('#drawer:not([hidden]) [role="tab"]').first().waitFor();
  const box = await drawerBox(page);
  expect(box.position, 'the drawer left the row at 1440').toBe('relative');
  expect(box.width, 'the drawer moved at 1440').toBeCloseTo(560, 0);
  expect(box.right).toBeCloseTo(1440, 0);
});

for (const scene of ['console-deployments', 'console-clusterroles']) {
  test(`at 900 no heading of ${scene} is cut`, async ({ page }) => {
    await page.setViewportSize({ width: 900, height: 800 });
    await show(page, scene);
    await page.locator('#panel thead th').first().waitFor();
    if (scene.endsWith('-open')) await page.locator('#drawer:not([hidden])').waitFor();
    const headings = await page.locator('#panel thead [data-label]').allTextContents();
    console.log(`900, ${scene}: ${headings.join(', ')}`);
    expect(headings.length, 'no headings drawn').toBeGreaterThan(2);
    expect(await cut(page, '#panel thead [data-label]'), 'a heading is cut').toEqual([]);
  });
}

/*
  A resource list never scrolls sideways: the name and the
  namespace give up width first, then other free text, then every column down
  to its heading, and only past that is every column narrower in proportion.
  No column is dropped. Headings stay whole wherever every heading fits:
  the list alone from 1280, and beside the drawer at 1920.
*/
for (const [width, scene, whole] of [
  [900, 'console-pods', false],
  [900, 'console-pods-open', false],
  [900, 'console-pods-arranged-open', false],
  [1280, 'console-pods', true],
  [1280, 'console-pods-open', false],
  [1440, 'console-pods', true],
  [1440, 'console-pods-open', false],
  [1920, 'console-pods', true],
  [1920, 'console-pods-open', true],
  [1280, 'console-deployments', true],
  [1280, 'console-clusterroles', true],
] as const) {
  test(`at ${String(width)} ${scene} draws every column and fits its panel`, async ({ page }) => {
    await page.setViewportSize({ width, height: 800 });
    await show(page, scene);
    await page.locator('#panel tbody tr[data-rowid]').first().waitFor();
    if (scene.endsWith('-open')) await page.locator('#drawer:not([hidden])').waitFor();
    const headings = (await page.locator('#panel thead [data-label]').allTextContents()).map((text) => text.trim().toLowerCase());
    if (scene.startsWith('console-pods')) {
      for (const label of ['name', 'namespace', 'status', 'restarts', 'cpu', 'memory', 'node', 'age']) {
        expect(headings, `${label} is not drawn`).toContain(label);
      }
    }
    const seen = await page.evaluate(() => {
      const panel = document.getElementById('panel');
      const box = panel?.querySelector<HTMLElement>('.table-scroll');
      const page = document.scrollingElement;
      if (panel === null || box == null || page === null) throw new Error('no list');
      return {
        panel: [panel.scrollWidth, panel.clientWidth],
        box: [box.scrollWidth, box.clientWidth],
        page: page.scrollWidth - page.clientWidth,
        widths: [...panel.querySelectorAll<HTMLElement>('thead th')].map(
          (head) => `${head.textContent.trim()} ${String(Math.round(head.getBoundingClientRect().width))}`,
        ),
      };
    });
    console.log(`${String(width)}, ${scene}: panel ${seen.panel.join(' in ')}; ${seen.widths.join(', ')}`);
    expect(seen.panel[0], 'the list scrolls sideways').toBeLessThanOrEqual(seen.panel[1] ?? 0);
    expect(seen.box[0], 'the table is wider than its box').toBeLessThanOrEqual(seen.box[1] ?? 0);
    expect(seen.page, 'the page scrolls sideways').toBeLessThanOrEqual(0);
    await expect(page.locator('[data-sideways]'), 'a sideways fade is drawn').toHaveCount(0);
    if (whole) expect(await cut(page, '#panel thead [data-label]'), 'a heading is cut').toEqual([]);
  });
}

test('at 1280 with the drawer open a cut name is offered whole on hover', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await show(page, 'console-pods-open');
  await page.locator('#drawer:not([hidden])').waitFor();
  const cell = page.locator('#panel tbody tr[data-rowid] td').first();
  await cell.waitFor();
  const [scroll, client] = await cell.evaluate((td) => {
    const text = td.querySelector<HTMLElement>('.truncate') ?? td;
    return [text.scrollWidth, text.clientWidth];
  });
  expect(scroll, 'the name is not cut at 1280 beside the drawer').toBeGreaterThan(client);
  await cell.hover();
  const id = await cell.evaluate((td) => td.closest('tr')?.getAttribute('data-rowid') ?? '');
  const title = await cell.getAttribute('title');
  expect(title, 'a cut name has no hover').not.toBeNull();
  expect(id.endsWith(title ?? '\u0000'), `the hover ${title ?? ''} is not the whole name of ${id}`).toBe(true);
});

test('at 390 the read-only manifest wraps between words: a timestamp reads whole on one line', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 800 });
  await show(page, 'console-drawer-secrets');
  await page.locator('#drawer:not([hidden]) [role="tab"]').first().waitFor();
  await page.locator('#drawer [role="tab"]', { hasText: 'Manifest' }).click();
  const stamp = page.locator('[data-manifest-lines] span', { hasText: MANIFEST_TIMESTAMP }).last();
  await stamp.waitFor();
  const lines = await stamp.evaluate((span) => new Set([...span.getClientRects()].map((rect) => Math.round(rect.top))).size);
  expect(lines, `${MANIFEST_TIMESTAMP} is drawn over several lines`).toBe(1);
  // And the line still wraps: the timestamp went under its key rather than
  // pushing the line past the drawer.
  const sideways = await page.evaluate(() => {
    const pre = document.querySelector<HTMLElement>('[data-manifest-lines]');
    return pre === null ? -1 : pre.scrollWidth - pre.clientWidth;
  });
  expect(sideways, 'the manifest scrolls sideways').toBeLessThanOrEqual(1);
});

test('at 390 the breadcrumb keeps its last part whole', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 800 });
  await show(page, 'console-pod-tall-page');
  const crumbs = page.locator('nav[aria-label="Breadcrumb"] li:not([aria-hidden])');
  const last = crumbs.last();
  // Wait for the pod's own part, not for any last part. Until the page has
  // declared itself the cluster is the breadcrumb's last part, drawn whole, so
  // a wait on "the last part" can resolve on it; on a loaded runner the page's
  // declaration then lands while the parts are read, and the count saw the
  // cluster alone, or nothing at all.
  await expect(last).toHaveText('prometheus-0');
  await expect(last).toBeVisible();
  const parts = await crumbs.evaluateAll((items) =>
    items.filter((item) => item.getBoundingClientRect().width > 0).map((item) => item.textContent?.trim() ?? ''),
  );
  console.log(`390, the pod's page: breadcrumb drawn as ${parts.join(' / ')}`);
  expect(parts, 'an earlier part is still drawn beside the last').toEqual(['prometheus-0']);
  const [scroll, client] = await last.evaluate((item) => [item.scrollWidth, item.clientWidth]);
  expect(scroll, `the breadcrumb's last part is cut: ${await last.textContent() ?? ''}`).toBeLessThanOrEqual(client + 1);
  expect(await last.textContent()).toBe('prometheus-0');
});

test('at 900 with the drawer open the name stays at the left edge of a table held wider than its panel', async ({ page }) => {
  await page.setViewportSize({ width: 900, height: 800 });
  await show(page, 'console-pods-open');
  await page.locator('#panel thead th').first().waitFor();
  await page.locator('#drawer:not([hidden])').waitFor();
  // A list fits its panel, so the overflow the held column is for is
  // made here: the table held at twice its panel.
  await page.locator('#panel table').evaluate((table) => {
    table.style.minWidth = '200%';
  });
  const panel = page.locator('#panel');
  const before = await panel.evaluate((box) => box.getBoundingClientRect().left);
  await panel.evaluate((box) => {
    box.scrollLeft = box.scrollWidth;
  });
  // The fades follow the table's resize and the scroll, a frame or two later.
  await page.locator('[data-sideways="before"]').waitFor();
  const held = await page.evaluate(() => {
    const box = document.getElementById('panel');
    const head = box?.querySelector<HTMLElement>('thead th');
    const cell = box?.querySelector<HTMLElement>('tbody tr[data-rowid] td');
    if (box === null || box === undefined || head == null || cell == null) throw new Error('no list');
    const at = (one: HTMLElement) => one.getBoundingClientRect();
    const h = at(head);
    const c = at(cell);
    const tick = cell.querySelector('input[type="checkbox"]');
    const t = tick?.getBoundingClientRect();
    const hit = document.elementFromPoint(c.left + c.width / 2, c.top + c.height / 2);
    // `elementFromPoint` skips the fade (`pointer-events-none`), so where it
    // starts is read from its box: over the held cell, it washes it out.
    const fade = document.querySelector<HTMLElement>('[data-sideways="before"]')?.getBoundingClientRect();
    return {
      scrolled: box.scrollLeft,
      head: h.left,
      cell: c.left,
      heading: head.textContent.trim().toLowerCase(),
      background: getComputedStyle(cell).backgroundColor,
      page: getComputedStyle(document.body).backgroundColor,
      onTop: hit !== null && cell.contains(hit),
      tickInside: t === undefined ? null : t.left >= c.left && t.right <= c.right,
      fade: fade === undefined ? null : fade.left,
      heldRight: c.right,
    };
  });
  console.log(`900, console-pods-open scrolled ${String(held.scrolled)}: name heading at ${px(held.head)}, cell at ${px(held.cell)}, panel at ${px(before)}, ${held.background}`);
  expect(held.scrolled, 'the panel did not scroll').toBeGreaterThan(0);
  expect(held.heading).toContain('name');
  expect(held.head, 'the name heading scrolled away').toBeCloseTo(before, 0);
  expect(held.cell, 'the name cell scrolled away').toBeCloseTo(before, 0);
  expect(held.onTop, 'a cell scrolled under the name paints over it').toBe(true);
  expect(held.background, 'the name cell is not the list\'s own surface').toBe(held.page);
  if (held.tickInside !== null) expect(held.tickInside, 'the tick is not inside the held cell').toBe(true);
  expect(held.fade, 'no left fade on a scrolled list').not.toBeNull();
  expect(held.fade ?? 0, 'the left fade covers the held cell').toBeGreaterThanOrEqual(held.heldRight - 1);

  // The row's ring is an outline on the <tr>, which the opaque held cell
  // paints over: the cell draws its share, the same width and color.
  await page.keyboard.press('Shift+Tab');
  const ring = await page.evaluate(() => {
    const row = document.querySelector<HTMLElement>('#panel tbody tr[data-rowid][tabindex="0"]');
    if (row === null) return null;
    row.focus();
    const cell = row.querySelector('td');
    if (cell === null) return null;
    const own = getComputedStyle(row);
    const share = getComputedStyle(cell, '::before');
    return {
      visible: row.matches(':focus-visible'),
      row: [own.outlineWidth, own.outlineColor],
      left: [share.borderLeftWidth, share.borderLeftColor],
      top: share.borderTopWidth,
      right: share.borderRightWidth,
    };
  });
  expect(ring, 'no row opens anything').not.toBeNull();
  if (ring === null) return;
  expect(ring.visible, 'the row does not show a keyboard ring').toBe(true);
  expect(ring.left, 'the held cell does not carry the row\'s ring').toEqual(ring.row);
  expect(ring.top).toBe(ring.row[0]);
  expect(ring.right).toBe('0px');
});
