import { expect, test, type Page } from '@playwright/test';
import { px, show } from './layoutkit';

/* -------------------------------------------------------------------------- */
/* 17 — the list's filter control and its row menus                          */
/* -------------------------------------------------------------------------- */

/**
 * The Pods list in the whole console, with a Node and a Status chosen in the
 * filter control, at 1024 and 1280.
 *
 * The rule is *no horizontal scrollbars*, and three things on this page could
 * break it: two chips and a funnel beside the search
 * box, a `⋯` in every row's name cell, and two menus drawn over the table. So:
 *
 * - **the document and the toolbar never scroll sideways**, before any menu is
 *   open and with each one open;
 * - **an arranged table** - stored widths, drawn as shares of the panel -
 *   **does not either**, `⋯` included: nothing on the page
 *   scrolls sideways at all;
 * - **an unarranged table is allowed exactly one scroller, the panel**, and
 *   that is not this test's: an unsized table declares its content's minimums
 *   and scrolls rather than squeezing (`declareWidth` in `DataGrid.tsx`). Eleven Pods columns are wider than a 1280px window's panel
 *   with or without the `⋯`; the width is printed, and opening a menu must add
 *   no second scroller;
 * - every row's `⋯` is wholly inside its own cell, which clips (`theme.css`): a `⋯` cut by its cell is a control nobody can find;
 * - the funnel's panel, a `⋯`'s menu, and a right-click's menu at the window's
 *   right edge are each wholly inside the window.
 *
 * Numbers with names in the messages, no screenshot, as above.
 */
async function sideways(page: Page): Promise<{ page: number; viewport: number; scrollers: string[] }> {
  return page.evaluate(() => {
    const scrollers = [...document.querySelectorAll<HTMLElement>('body *')]
      .filter((element) => {
        const overflow = getComputedStyle(element).overflowX;
        return (overflow === 'auto' || overflow === 'scroll') && element.scrollWidth > element.clientWidth + 1;
      })
      .map((element) => (element.id === '' ? element.tagName.toLowerCase() : `#${element.id}`));
    return { page: document.documentElement.scrollWidth, viewport: window.innerWidth, scrollers };
  });
}

async function inside(
  page: Page,
  what: string,
  box: { x: number; y: number; width: number; height: number } | null,
): Promise<void> {
  if (box === null) throw new Error(`${what} is not drawn`);
  const size = page.viewportSize();
  if (size === null) throw new Error('no viewport');
  expect(box.x, `${what} starts ${px(-box.x)} left of the window`).toBeGreaterThanOrEqual(0);
  expect(box.y, `${what} starts ${px(-box.y)} above the window`).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width, `${what} ends ${px(box.x + box.width - size.width)} past the right edge`).toBeLessThanOrEqual(
    size.width,
  );
  expect(box.y + box.height, `${what} ends ${px(box.y + box.height - size.height)} below the window`).toBeLessThanOrEqual(
    size.height,
  );
}

for (const arranged of [true, false]) {
  for (const width of [1024, 1280]) {
    test(`the ${arranged ? 'arranged' : 'unarranged'} Pods list with filter chips and row menus adds no sideways scroll at ${String(width)}px`, async ({
      page,
    }) => {
      await page.setViewportSize({ width, height: 800 });
      await page.goto(`/?scene=${arranged ? 'console-pods-arranged' : 'console-pods'}`);
      const funnel = page.getByRole('button', { name: /^Filter by column/ });
      await funnel.waitFor();
      await page.getByRole('button', { name: 'Remove the filter Node: kubeworker1' }).waitFor();
      await page.getByRole('button', { name: 'Remove the filter Status: Running' }).waitFor();
      const more = page.locator('[data-row-menu]');
      await expect(more).toHaveCount(5);

      const at = await page.evaluate(() => {
        const panel = document.getElementById('panel');
        const header = document.querySelector('main header');
        const table = document.querySelector('#panel .kt-grid');
        if (panel === null || !(header instanceof HTMLElement) || table === null) {
          throw new Error('no panel, toolbar or table');
        }
        // Every row's ⋯ against the cell that clips it.
        const cut = [...document.querySelectorAll('[data-row-menu]')].flatMap((button) => {
          const cell = button.closest('td');
          if (cell === null) return ['a ⋯ outside any cell'];
          const own = button.getBoundingClientRect();
          const box = cell.getBoundingClientRect();
          return own.left >= box.left - 0.5 && own.right <= box.right + 0.5 && own.width > 0
            ? []
            : [`${String(Math.round(own.left))}-${String(Math.round(own.right))} in ${String(Math.round(box.left))}-${String(Math.round(box.right))}`];
        });
        return {
          panel: panel.clientWidth,
          table: table.getBoundingClientRect().width,
          header: header.clientWidth,
          headerScroll: header.scrollWidth,
          headerHeight: header.getBoundingClientRect().height,
          cut,
        };
      });
      console.log(
        `${arranged ? 'arranged' : 'unarranged'} pods list at ${String(width)}px: panel ${px(at.panel)}, ` +
          `table ${px(at.table)}, toolbar ${px(at.header)} wide and ${px(at.headerHeight)} tall`,
      );
      expect(at.cut, 'a row’s ⋯ is cut by its cell').toEqual([]);
      expect(at.headerScroll, 'the toolbar is wider than its box').toBeLessThanOrEqual(at.header);

      // What may scroll sideways here, and nothing else ever may.
      const allowed = arranged ? [] : ['#panel'];
      const check = async (when: string): Promise<void> => {
        const now = await sideways(page);
        expect(now.page, `the page scrolls sideways ${when}`).toBeLessThanOrEqual(now.viewport);
        expect(
          now.scrollers.filter((one) => !allowed.includes(one)),
          `a box scrolls sideways ${when}`,
        ).toEqual([]);
      };
      await check('with no menu open');
      if (arranged) expect(at.table, 'the arranged table is wider than its panel').toBeLessThanOrEqual(at.panel);

      // The funnel's panel.
      await funnel.click();
      const filters = page.getByRole('group', { name: 'Filter by column' });
      await inside(page, 'the filter panel', await filters.boundingBox());
      await check('with the filter panel open');
      await page.keyboard.press('Escape');
      await expect(filters).toHaveCount(0);

      // The last row's ⋯, then a right-click on that row as far right as the
      // window shows it - where a menu drawn right of the pointer would leave
      // the window.
      await more.last().click();
      const menu = page.getByRole('menu');
      await expect(page.getByRole('menuitem', { name: 'Delete' })).toBeVisible();
      await inside(page, 'the ⋯ menu', await menu.boundingBox());
      await check('with the ⋯ menu open');
      await page.keyboard.press('Escape');
      await expect(menu).toHaveCount(0);

      const last = await page.locator('tbody tr').last().boundingBox();
      if (last === null) throw new Error('no last row');
      await page.mouse.click(Math.min(width - 12, last.x + last.width - 4), last.y + last.height - 3, { button: 'right' });
      await expect(page.getByRole('menuitem', { name: 'Delete' })).toBeVisible();
      await inside(page, 'the right-click menu', await menu.boundingBox());
      await check('with the right-click menu open');
      await page.keyboard.press('Escape');
    });
  }
}

/* -------------------------------------------------------------------------- */
/* 17, continued — the row menu                                              */
/* -------------------------------------------------------------------------- */

/**
 * The row menu's four rules, in a real engine:
 *
 * 1. **The ⋯ is drawn only on the row under the pointer or holding the focus**,
 *    and its room stays reserved: hovering a row moves nothing in it.
 * 2. **Header, safe entries, separator, verbs**: Open, Logs, Shell, Copy name,
 *    a line, then the drawer's verbs.
 * 3. **Only the irreversible verb is red in the menu**: Delete alone carries
 *    the menu's mark and its color is not Evict's or Restart's, though all
 *    three are destructive and the drawer still reds all three.
 * 4. **A right-click opens at the pointer; the ⋯, the menu key and Shift+F10
 *    open under the ⋯.** Real Chromium sends the menu key's contextmenu at the
 *    focused element's coordinates, which jsdom cannot say.
 *
 * And the browser's own menu: never drawn over the open menu.
 */
async function contextmenus(page: Page): Promise<{ prevented: boolean; on: string }[]> {
  return page.evaluate(() => (window as unknown as { kt149?: { prevented: boolean; on: string }[] }).kt149 ?? []);
}

async function recordContextmenus(page: Page): Promise<void> {
  // On the window, in the bubble phase: after React's own listener at the
  // root, so `defaultPrevented` is what the page decided.
  await page.evaluate(() => {
    const seen: { prevented: boolean; on: string }[] = [];
    (window as unknown as { kt149: typeof seen }).kt149 = seen;
    window.addEventListener('contextmenu', (event) => {
      const target = event.target instanceof Element ? event.target : null;
      seen.push({
        prevented: event.defaultPrevented,
        on: target === null ? '' : `${target.tagName.toLowerCase()}${target.closest('[role="menu"]') === null ? '' : ' in the menu'}`,
      });
    });
  });
}

for (const width of [1024, 1280]) {
  test(`the row menu at ${String(width)}px: shown on hover, its entries, one red verb, and where it opens`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 800 });
    await page.goto('/?scene=console-pods-arranged');
    const more = page.locator('[data-row-menu]');
    await expect(more).toHaveCount(5);
    await recordContextmenus(page);
    await page.mouse.move(1, 1);

    // 1. Hidden until hovered, and nothing moves when it appears.
    const second = page.locator('tbody tr').nth(1);
    const button = more.nth(1);
    const before = await button.boundingBox();
    const resting = await button.evaluate((element) => getComputedStyle(element).opacity);
    await second.hover();
    await expect.poll(async () => button.evaluate((element) => getComputedStyle(element).opacity)).toBe('1');
    const after = await button.boundingBox();
    expect(resting, 'the ⋯ is drawn on a row nobody is pointing at').toBe('0');
    expect(after?.x, 'the ⋯ moved when it appeared').toBe(before?.x);
    expect(after?.width, 'the ⋯ changed size when it appeared').toBe(before?.width);
    expect(
      await more.nth(2).evaluate((element) => getComputedStyle(element).opacity),
      'another row’s ⋯ is drawn',
    ).toBe('0');

    // 4. A right-click: at the pointer.
    const row = await second.boundingBox();
    if (row === null) throw new Error('no second row');
    const pointer = { x: row.x + 200, y: row.y + row.height / 2 };
    await page.mouse.click(pointer.x, pointer.y, { button: 'right' });
    const menu = page.getByRole('menu');
    await expect(page.getByRole('menuitem', { name: 'Delete' })).toBeVisible();
    const atPointer = await menu.boundingBox();
    expect(Math.abs((atPointer?.x ?? 0) - pointer.x), 'a right-click’s menu is not at the pointer').toBeLessThanOrEqual(1);
    expect(Math.abs((atPointer?.y ?? 0) - pointer.y), 'a right-click’s menu is not at the pointer').toBeLessThanOrEqual(1);

    // 2. Its entries, in order, the separator as `|`.
    const entries = await menu.evaluate((element) =>
      [...element.querySelectorAll('[role="menuitem"], [role="separator"]')].map((one) =>
        one.getAttribute('role') === 'separator' ? '|' : (one.textContent ?? '').trim(),
      ),
    );
    expect(entries).toEqual(['Open', 'Logs', 'Shell', 'Copy name', '|', 'Evict', 'Restart', 'Delete']);
    expect(await menu.locator('p').first().textContent()).toBe('payments/ledger-api-7d9f8b6c4-x2k9p');

    // 3. One red verb, and its color is its own.
    const red = await menu.locator('[data-destructive]').allTextContents();
    expect(red.map((one) => one.trim())).toEqual(['Delete']);
    const colour = (name: string) =>
      page.getByRole('menuitem', { name }).evaluate((element) => getComputedStyle(element).color);
    expect(await colour('Evict'), 'Evict is drawn in Delete’s color').not.toBe(await colour('Delete'));
    expect(await colour('Restart')).toBe(await colour('Evict'));

    // L3: a right-click on the open menu draws nothing of the browser's.
    const header = await menu.locator('p').first().boundingBox();
    if (header === null) throw new Error('no header in the menu');
    await page.mouse.click(header.x + 4, header.y + header.height / 2, { button: 'right' });
    const onMenu = (await contextmenus(page)).filter((one) => one.on.endsWith('in the menu'));
    expect(onMenu.length, 'no contextmenu reached the open menu').toBeGreaterThan(0);
    expect(onMenu.every((one) => one.prevented), 'the browser’s menu was drawn over the open one').toBe(true);
    await page.keyboard.press('Escape');
    await expect(menu).toHaveCount(0);

    // 4. The ⋯, Shift+F10 and the menu key: all three under the ⋯.
    const placed: string[] = [];
    const where = async (): Promise<string> => {
      const box = await menu.boundingBox();
      return `${String(Math.round(box?.x ?? -1))},${String(Math.round(box?.y ?? -1))}`;
    };
    await button.click();
    await expect(page.getByRole('menuitem', { name: 'Delete' })).toBeVisible();
    const under = await button.boundingBox();
    placed.push(await where());
    await page.keyboard.press('Escape');
    await second.focus();
    await page.keyboard.press('Shift+F10');
    await expect(page.getByRole('menuitem', { name: 'Delete' })).toBeVisible();
    placed.push(await where());
    await page.keyboard.press('Escape');
    await second.focus();
    await page.keyboard.press('ContextMenu');
    await expect(page.getByRole('menuitem', { name: 'Delete' })).toBeVisible();
    placed.push(await where());
    await page.keyboard.press('Escape');
    console.log(`row menu at ${String(width)}px: right-click at ${String(Math.round(pointer.x))},${String(Math.round(pointer.y))}; ⋯, Shift+F10, menu key at ${placed.join(' / ')}`);
    expect(placed[0], 'the ⋯ menu is not under the ⋯').toBe(
      `${String(Math.round(under?.x ?? -2))},${String(Math.round((under?.y ?? 0) + (under?.height ?? 0)))}`,
    );
    expect(new Set(placed).size, `the ⋯, Shift+F10 and the menu key opened at ${placed.join(' / ')}`).toBe(1);
  });
}

/**
 * Review L6: a link inside a row keeps the browser's menu, from the pointer and
 * from the keyboard, in a real engine. An Ingress host is the case.
 */
test('a link inside a row keeps the browser’s menu, from the pointer and from the keyboard', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto('/?scene=console-ingresses');
  const link = page.locator('tbody a[href="https://shop.example.com"]');
  await link.waitFor();
  await recordContextmenus(page);

  await link.click({ button: 'right' });
  await link.focus();
  await page.keyboard.press('Shift+F10');
  await link.focus();
  await page.keyboard.press('ContextMenu');
  await expect(page.getByRole('menu')).toHaveCount(0);
  const onLink = (await contextmenus(page)).filter((one) => one.on === 'a');
  expect(onLink.length, 'no contextmenu reached the link').toBeGreaterThan(0);
  expect(onLink.some((one) => one.prevented), 'the browser’s menu was taken from a link').toBe(false);

  // The rest of the same row is still the row's.
  const row = await page.locator('tbody tr').first().boundingBox();
  if (row === null) throw new Error('no row');
  await page.mouse.click(row.x + 60, row.y + row.height / 2, { button: 'right' });
  await expect(page.getByRole('menuitem', { name: 'Delete' })).toBeVisible();
});

/* -------------------------------------------------------------------------- */
/* 19 — the list page under a drawer, wider than its panel                   */
/* -------------------------------------------------------------------------- */

/**
 * Three rules, on the Pods list in the whole console:
 *
 * - at 1024 with a drawer open, every control in the list's toolbar is drawn
 *   whole, left of the drawer, and is what the pointer finds at its own center;
 * - a table wider than its panel says so at the cut: a fade at the
 *   panel's right edge, gone once scrolled to the end and moved to the left,
 *   and none at all where the table fits;
 * - a heading or a cell its column cuts offers the whole text on
 *   hover, one that is whole offers nothing, and no pill is cut by a column
 *   sized for its text alone.
 */
for (const width of [1024, 1280]) {
  test(`the toolbar is whole and left of an open drawer at ${String(width)}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 800 });
    await page.goto('/?scene=console-pods-open');
    await page.locator('#drawer:not([hidden])').waitFor();
    await page.getByRole('button', { name: 'More table options' }).waitFor();

    const at = await page.evaluate(() => {
      const drawer = document.getElementById('drawer');
      const header = document.querySelector('main header');
      if (drawer === null || !(header instanceof HTMLElement)) throw new Error('no drawer or toolbar');
      const edge = drawer.getBoundingClientRect().left;
      const controls = [...header.querySelectorAll<HTMLElement>('button, input')].map((control) => {
        const box = control.getBoundingClientRect();
        const found = document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 2);
        return {
          name: (
            control.getAttribute('aria-label') ??
            control.getAttribute('placeholder') ??
            control.textContent ??
            ''
          ).trim(),
          left: box.left,
          right: box.right,
          width: box.width,
          found: found !== null && (control === found || control.contains(found)),
        };
      });
      return { edge, controls, scroll: header.scrollWidth, client: header.clientWidth };
    });
    console.log(
      `toolbar under a drawer at ${String(width)}px: drawer from ${px(at.edge)}, ` +
        at.controls.map((one) => `${one.name.slice(0, 18)} ${px(one.left)}-${px(one.right)}`).join(', '),
    );
    expect(at.controls.length, 'no controls in the toolbar').toBeGreaterThanOrEqual(5);
    for (const control of at.controls) {
      expect(control.right, `${control.name} ends ${px(control.right - at.edge)} under the drawer`).toBeLessThanOrEqual(
        at.edge + 0.5,
      );
      expect(control.found, `${control.name}: the pointer at its center finds something else`).toBe(true);
    }
    const field = at.controls.find((one) => one.name.startsWith('search all columns'));
    expect(field?.width ?? 0, 'the search box is squeezed past typing into').toBeGreaterThanOrEqual(96);
    expect(at.scroll, 'the toolbar is wider than its box').toBeLessThanOrEqual(at.client);
  });
}

/**
 * Scene 19, the list page's kinds column: the longest built-in kind's
 * name is one line in the 192px column, the height of every other row.
 */
test('a long kind name is one row high in the kinds column', async ({ page }) => {
  await page.setViewportSize({ width: 1400, height: 900 });
  await show(page, 'menu');
  await page.route(
    (url) => /^\/api\/c\/[^/]+\/nav$/.test(url.pathname),
    (route) =>
      route.fulfill({
        json: {
          state: 'ok',
          data: {
            groups: [
              {
                key: 'workloads',
                label: 'Workloads',
                deferred: false,
                items: [
                  { text: 'Pods', href: '/c/demo/pods' },
                  { text: 'Horizontal pod autoscalers', href: '/c/demo/horizontalpodautoscalers' },
                ],
              },
            ],
          },
        },
      }),
  );
  await page.reload();
  const long = page.locator('#menu a[data-kind="horizontalpodautoscalers"]');
  await long.waitFor({ state: 'attached' });
  if (!(await long.isVisible())) await page.locator('details[data-group="workloads"] > summary').click();
  const heights = await page.evaluate(() =>
    [...document.querySelectorAll('#menu a[data-kind]')].map((row) => row.getBoundingClientRect().height),
  );
  expect(heights.length, 'the two rows are not drawn').toBe(2);
  expect(
    Math.abs((heights[1] ?? 0) - (heights[0] ?? 0)),
    `the long name's row is ${px(heights[1] ?? 0)} high against ${px(heights[0] ?? 0)}`,
  ).toBeLessThanOrEqual(0.5);
});

/*
  The list's table meets the panel's edges: `#panel` has no padding, so no
  band opens above the heading row or beside it. The heading's top is the toolbar's bottom border, its left and right are the
  panel's, at rest and once the rows have scrolled under it.
*/
test('the heading row meets the toolbar and both edges of the panel, at rest and after scrolling', async ({ page }) => {
  await page.setViewportSize({ width: 1700, height: 240 });
  await page.goto('/?scene=console-pods-arranged');
  await page.locator('#panel thead th').first().waitFor();

  const read = (): Promise<{ toolbar: number; panel: DOMRect; first: DOMRect; last: DOMRect; scrolled: number }> =>
    page.evaluate(() => {
      const panel = document.getElementById('panel');
      const cells = [...document.querySelectorAll('#panel thead th')];
      if (panel === null || cells.length === 0) throw new Error('no heading row under #panel');
      const box = panel.getBoundingClientRect();
      // The list's own toolbar: the header whose bottom is closest above the panel.
      const toolbar = Math.max(
        ...[...document.querySelectorAll('header')]
          .map((header) => header.getBoundingClientRect().bottom)
          .filter((bottom) => bottom <= box.top + 0.5),
      );
      return {
        toolbar,
        panel: box.toJSON() as DOMRect,
        first: cells[0]!.getBoundingClientRect().toJSON() as DOMRect,
        last: cells.at(-1)!.getBoundingClientRect().toJSON() as DOMRect,
        scrolled: panel.scrollTop,
      };
    });

  const rest = await read();
  console.log(
    `at rest: toolbar ${px(rest.toolbar)}, heading ${px(rest.first.top)}; panel ${px(rest.panel.left)}–${px(rest.panel.right)}, heading ${px(rest.first.left)}–${px(rest.last.right)}`,
  );
  expect(rest.first.top - rest.toolbar, 'a band between the toolbar and the heading row').toBeCloseTo(0, 0);
  expect(rest.first.top - rest.panel.top, 'the heading row starts below the panel').toBeCloseTo(0, 0);
  expect(rest.first.left - rest.panel.left, 'a band left of the heading row').toBeCloseTo(0, 0);
  // `clientWidth`, not the box: a vertical scrollbar takes its own width.
  const inner = await page.evaluate(() => {
    const panel = document.getElementById('panel');
    return panel === null ? 0 : panel.getBoundingClientRect().left + panel.clientWidth;
  });
  expect(inner - rest.last.right, 'a band right of the heading row').toBeCloseTo(0, 0);

  await page.locator('#panel').evaluate((panel) => {
    panel.scrollTop = panel.scrollHeight;
  });
  const scrolled = await read();
  expect(scrolled.scrolled, 'the panel did not scroll, so nothing here is tested').toBeGreaterThan(0);
  expect(scrolled.first.top - scrolled.panel.top, 'the sticky heading left the panel top once scrolled').toBeCloseTo(0, 0);
});
