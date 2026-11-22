import { expect, test, type Page } from '@playwright/test';
import { px, show, stored } from './layoutkit';

/* -------------------------------------------------------------------------- */
/* 3 — the cluster rail is 56px at every width, and has no fold             */
/* -------------------------------------------------------------------------- */

async function widthOf(page: Page, selector: string): Promise<number> {
  return page.locator(selector).evaluate((element) => element.getBoundingClientRect().width);
}

/**
 * The rail is always the compact strip, with no fold control and no stored
 * width: what is measured is that 56px is the only width, at a desktop width
 * and at the narrowest this file measures the rail at.
 */
for (const width of [1280, 500]) {
  test(`the cluster rail is 56px at ${String(width)}px, with no fold control, and after a reload`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 });
    await show(page, 'rail');

    const drawn = await widthOf(page, '#rail');
    expect(drawn, `the rail at ${String(width)}px is drawn ${px(drawn)} and should be 56px`).toBeCloseTo(
      56,
      0,
    );
    await expect(page.locator('[aria-controls="rail"]'), 'a fold control is still drawn').toHaveCount(0);
    await expect(page.locator('#rail[data-folded]')).toHaveCount(0);

    await page.reload();
    await page.waitForSelector('#rail');
    const afterReload = await widthOf(page, '#rail');
    expect(afterReload, `after a reload the rail is ${px(afterReload)}`).toBeCloseTo(56, 0);
    // Nothing is remembered, because there is nothing to choose.
    expect(await stored(page, 'kubetower.rail.folded'), 'the rail wrote a fold key').toBeNull();
  });
}

/* -------------------------------------------------------------------------- */
/* 4 — the kinds column is shown or hidden, and scrolls inside itself        */
/* -------------------------------------------------------------------------- */

/** Thirty-six kinds in one group: what a real cluster's built-in menu runs to. */
const TALL_NAV = {
  state: 'ok',
  data: {
    groups: [
      {
        key: 'workloads',
        label: 'Workloads',
        deferred: false,
        items: Array.from({ length: 36 }, (_value, index) => ({
          text: `Kind ${String(index + 1)}`,
          href: `/c/demo/kind${String(index + 1)}`,
        })),
      },
    ],
  },
};

/**
 * The column is shown at 192px or hidden at none, toggled by
 * Ctrl+B here (the scene has no top bar and no rail; the *Kinds* button is
 * the same store, and is in `Menu.test.tsx` and case 18). The choice is
 * `kubetower.menu.mode`.
 */
test('the kinds column is 192px shown, takes no width hidden, and stays hidden after a reload', async ({
  page,
}) => {
  await page.setViewportSize({ width: 1400, height: 900 });
  await show(page, 'menu');

  const shown = await widthOf(page, '#menu');
  expect(shown, `the column shown is drawn ${px(shown)} and should be 192px`).toBeCloseTo(192, 0);
  await expect(page.locator('#menu button[aria-controls="menu"]'), 'a fold control is still drawn').toHaveCount(0);

  await page.locator('#menu-jump').focus();
  await page.locator('#menu-jump').blur();
  await page.keyboard.press('Control+b');
  const hidden = await widthOf(page, '#menu');
  expect(hidden, `the column hidden is drawn ${px(hidden)} and should take no width`).toBe(0);
  await expect(page.locator('#menu[data-hidden]')).toHaveCount(1);

  await page.reload();
  await page.waitForSelector('#menu', { state: 'attached' });
  const afterReload = await widthOf(page, '#menu');
  expect(afterReload, `hidden did not survive the reload: ${px(afterReload)}`).toBe(0);
  expect(await stored(page, 'kubetower.menu.mode'), 'the column stores its mode').toBe('hidden');
  expect(await stored(page, 'kubetower.menu.folded'), 'nothing writes the old fold key').toBeNull();
});

test('thirty-six kinds scroll inside the column, not the page, at 900px high', async ({ page }) => {
  await page.setViewportSize({ width: 1400, height: 900 });
  await show(page, 'menu');
  // Registered after the scene's own answers, so it is asked first.
  await page.route(
    (url) => /^\/api\/c\/[^/]+\/nav$/.test(url.pathname),
    (route) => route.fulfill({ json: TALL_NAV }),
  );
  await page.reload();
  await page.locator('details[data-group="workloads"] > summary').click();
  await page.locator('#menu a[data-kind]').nth(35).waitFor({ state: 'attached' });

  const reading = await page.evaluate(() => {
    const menu = document.getElementById('menu');
    const scroller = menu?.querySelector<HTMLElement>('.overflow-y-auto');
    if (menu === null || scroller === null || scroller === undefined) throw new Error('no column scroller');
    return {
      rows: menu.querySelectorAll('a[data-kind]').length,
      scrollHeight: scroller.scrollHeight,
      clientHeight: scroller.clientHeight,
      documentScrollHeight: document.documentElement.scrollHeight,
      documentClientHeight: document.documentElement.clientHeight,
      menuBottom: menu.getBoundingClientRect().bottom,
    };
  });
  expect(reading.rows, 'the column draws the thirty-six rows').toBe(36);
  expect(
    reading.scrollHeight,
    `the rows (${String(reading.scrollHeight)}px) fit the column's ${String(reading.clientHeight)}px, so nothing scrolls inside it`,
  ).toBeGreaterThan(reading.clientHeight);
  expect(
    reading.documentScrollHeight - reading.documentClientHeight,
    `the page scrolls instead of the column: ${String(reading.documentScrollHeight)} against ${String(reading.documentClientHeight)}`,
  ).toBeLessThanOrEqual(0);
  expect(reading.menuBottom, `the column runs to ${px(reading.menuBottom)}, past the 900px window`).toBeLessThanOrEqual(900.5);
});

/* -------------------------------------------------------------------------- */
/* 7 — everything on the rail fits its 56px                                  */
/* -------------------------------------------------------------------------- */

/**
 * The seventh, and the argument for it rather than a `Rail.test.tsx` case.
 *
 * jsdom loads no stylesheet, so there it is a string comparison: a control whose
 * glyph overruns its column by twelve pixels has exactly the same DOM as one
 * that fits. **An overrun is only visible where a real engine lays out the
 * real stylesheet**, which is what this file is.
 *
 * The rail is 56px at every width and the tiles are 36px squares with a state badge hanging off their corner, so the same reading
 * is taken at 1280px as well as at 500px, and it covers the tiles - the badge
 * and the active ring are the two things that reach past a tile's own box.
 *
 * The rail carries a 1px right border, which is why 56px of border box is 55px
 * of client box.
 */
for (const width of [1280, 500]) {
  test(`every tile and every control on the rail fits its 56px at ${String(width)}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await show(page, 'rail');
    await page.waitForSelector('#rail [data-cluster]');

    const reading = await page.evaluate(() => {
      const rail = document.querySelector('#rail');
      if (!(rail instanceof HTMLElement)) throw new Error('no #rail');
      const box = rail.getBoundingClientRect();
      const boxes = [
        ...rail.querySelectorAll('button'),
        ...rail.querySelectorAll('[data-cluster]'),
        ...rail.querySelectorAll('[data-badge]'),
      ].map((element) => ({
        name:
          element.getAttribute('title') ??
          element.getAttribute('data-badge') ??
          element.textContent ??
          '(unnamed)',
        left: element.getBoundingClientRect().left,
        right: element.getBoundingClientRect().right,
        // A tile's own scroll width counts its badge, which hangs 2px past the
        // square's corner on purpose; the tile's border box is checked above
        // and the badge's own box beside it, so only a button's text is read
        // against its room.
        scrollWidth: element instanceof HTMLButtonElement ? element.scrollWidth : 0,
        clientWidth: element instanceof HTMLButtonElement ? element.clientWidth : 0,
      }));
      return {
        left: box.left,
        right: box.right,
        scrollWidth: rail.scrollWidth,
        clientWidth: rail.clientWidth,
        documentScrollWidth: document.documentElement.scrollWidth,
        documentClientWidth: document.documentElement.clientWidth,
        boxes,
      };
    });

    expect(
      reading.scrollWidth - reading.clientWidth,
      `the rail scrolls sideways — scrollWidth ${String(reading.scrollWidth)} against clientWidth ${String(reading.clientWidth)}`,
    ).toBeLessThanOrEqual(0);
    expect(
      reading.documentScrollWidth - reading.documentClientWidth,
      `the document scrolls sideways — ${String(reading.documentScrollWidth)} against ${String(reading.documentClientWidth)}`,
    ).toBeLessThanOrEqual(0);
    expect(reading.boxes.length, 'nothing on the rail was measured').toBeGreaterThan(2);
    // Kinds is at the foot, and measured with the rest.
    expect(
      reading.boxes.some((one) => one.name.includes('kinds column')),
      'the Kinds button is not on the rail',
    ).toBe(true);

    for (const one of reading.boxes) {
      expect(
        one.right,
        `${one.name}: its right edge is ${px(one.right)}, outside the rail's ${px(reading.right)}`,
      ).toBeLessThanOrEqual(reading.right + 0.5);
      expect(
        one.left,
        `${one.name}: its left edge is ${px(one.left)}, outside the rail's ${px(reading.left)}`,
      ).toBeGreaterThanOrEqual(reading.left - 0.5);
      // The half a border box cannot show: `Narrow` once fitted its box and
      // printed 51px of word into 39px of room.
      expect(
        one.scrollWidth - one.clientWidth,
        `${one.name}: it is printing ${String(one.scrollWidth)}px into ${String(one.clientWidth)}px of room`,
      ).toBeLessThanOrEqual(0);
    }
  });
}

/**
 * 18 — Kinds at the foot of the rail.
 *
 * The whole console, on the overview fixture's three clusters, so the rail is
 * drawn by `Shell` and not by a scene: Kinds is inside the rail's 56px, below
 * Arrange and above Log out, and the top bar does not carry it - the
 * breadcrumb starts at the top bar's left padding.
 */
for (const width of [1280, 500]) {
  test(`Kinds is at the foot of the rail, below Arrange, and not in the top bar, at ${String(width)}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.goto('/?scene=overview');
    const kinds = page.locator('#rail button[aria-label="Kinds"]');
    await kinds.waitFor();
    await page.locator('#rail button[aria-label="Log out"]').waitFor();

    const reading = await page.evaluate(() => {
      const box = (selector: string) => {
        const element = document.querySelector(selector);
        if (!(element instanceof HTMLElement)) throw new Error(`no ${selector}`);
        const rect = element.getBoundingClientRect();
        return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom };
      };
      const topbar = document.getElementById('topbar');
      const crumbs = topbar?.querySelector('nav[aria-label="Breadcrumb"]');
      return {
        rail: box('#rail'),
        arrange: box('#rail button[aria-label="Arrange"]'),
        kinds: box('#rail button[aria-label="Kinds"]'),
        signOut: box('#rail button[aria-label="Log out"]'),
        inTopbar: topbar?.querySelectorAll('button[aria-controls="menu"]').length ?? -1,
        topbarLeft: topbar?.getBoundingClientRect().left ?? 0,
        crumbsLeft: crumbs?.getBoundingClientRect().left ?? 0,
      };
    });

    expect(reading.inTopbar, 'the top bar still carries a Kinds button').toBe(0);
    expect(reading.kinds.left, `Kinds starts at ${px(reading.kinds.left)}, left of the rail`).toBeGreaterThanOrEqual(
      reading.rail.left - 0.5,
    );
    expect(reading.kinds.right, `Kinds ends at ${px(reading.kinds.right)}, past the rail's ${px(reading.rail.right)}`).toBeLessThanOrEqual(
      reading.rail.right + 0.5,
    );
    expect(reading.kinds.top, 'Kinds is not below Arrange').toBeGreaterThanOrEqual(reading.arrange.bottom);
    expect(reading.signOut.top, 'Log out is not below Kinds').toBeGreaterThanOrEqual(reading.kinds.bottom);
    // The foot is at the bottom of the window: the rail is the window's height.
    expect(reading.kinds.bottom, 'Kinds is outside the window').toBeLessThanOrEqual(900);
    // 12px of padding (`px-3`) and nothing else before the breadcrumb.
    expect(
      reading.crumbsLeft - reading.topbarLeft,
      `the breadcrumb starts ${px(reading.crumbsLeft - reading.topbarLeft)} into the top bar`,
    ).toBeLessThanOrEqual(12.5);
  });
}

/**
 * 18b — the in-cluster console's top bar.
 *
 * One cluster served in a cluster: no rail, Kinds in the bottom left corner,
 * the kinds column drawn on a wide window and folded on a narrow one - and the
 * session in the top bar. A smaller line box centered in a taller one sits with
 * its baseline visibly higher, so this pins the signed-in name - the button
 * that holds Settings and Log out - to the breadcrumb's size and line, the
 * button to the header's middle, and the menu it opens - Settings and Log out,
 * nothing else - inside the window and under the name's right edge, at 1440
 * and at 500. Each item draws its glyph before its label, at the console's
 * icon size and on the label's middle.
 */
for (const width of [1440, 500]) {
  test(`the in-cluster top bar draws the signed-in name as the header's line, opens its menu inside the window, and draws Kinds in the corner, at ${String(width)}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.goto('/?scene=in-cluster');
    const account = page.locator('#topbar button[aria-haspopup="menu"]');
    await account.waitFor();
    await page.locator('#topbar button[title="Signed in as alice"]').waitFor();
    // Attached, not visible: below 640px the column starts folded.
    await page.locator('#menu-jump').waitFor({ state: 'attached' });

    const reading = await page.evaluate(() => {
      const text = (selector: string) => {
        const element = document.querySelector(selector);
        if (!(element instanceof Element)) throw new Error(`no ${selector}`);
        const rect = element.getBoundingClientRect();
        const style = getComputedStyle(element);
        return { top: rect.top, bottom: rect.bottom, size: style.fontSize, line: style.lineHeight };
      };
      return {
        topbar: text('#topbar'),
        crumbs: text('#topbar nav[aria-label="Breadcrumb"] ol'),
        account: text('#topbar button[aria-haspopup="menu"]'),
        nameText: text('#topbar button[aria-haspopup="menu"] .truncate'),
        menus: document.querySelectorAll('#topbar button[aria-haspopup="menu"]').length,
        settingsLinks: document.querySelectorAll('#topbar a[href="/settings"]').length,
        kinds: document.querySelectorAll('button[aria-controls="menu"]').length,
        corner: (() => {
          const at = document.querySelector('button[data-kinds-at="corner"]')?.getBoundingClientRect();
          return at === undefined ? null : { left: at.left, bottom: at.bottom, height: innerHeight };
        })(),
        rail: document.getElementById('rail') !== null,
        menuShown: getComputedStyle(document.getElementById('menu') as HTMLElement).display !== 'none',
      };
    });

    expect(reading.kinds, 'not one Kinds toggle on the in-cluster console').toBe(1);
    expect(reading.corner, 'the Kinds toggle is not the corner one').not.toBeNull();
    expect(reading.corner!.left, 'Kinds is not at the left edge').toBeLessThan(16);
    expect(reading.corner!.height - reading.corner!.bottom, 'Kinds is not at the bottom').toBeLessThan(16);
    expect(reading.rail, 'a rail is drawn for one cluster').toBe(false);
    // Folded by default below 640px, drawn above it.
    expect(reading.menuShown, 'the kinds column is not drawn as the width wants').toBe(width >= 640);
    expect(reading.menus, 'the top bar has one menu button, the name').toBe(1);
    expect(reading.settingsLinks, 'a standalone Settings link is still drawn').toBe(0);
    expect(reading.nameText.size, `the name is ${reading.nameText.size}, the breadcrumb ${reading.crumbs.size}`).toBe(
      reading.crumbs.size,
    );
    expect(reading.nameText.line, `the name's line is ${reading.nameText.line}`).toBe(reading.account.line);
    const middle = (box: { top: number; bottom: number }) => (box.top + box.bottom) / 2;
    expect(
      Math.abs(middle(reading.account) - middle(reading.topbar)),
      `the name's box is centered at ${px(middle(reading.account))}, the top bar at ${px(middle(reading.topbar))}`,
    ).toBeLessThanOrEqual(0.5);

    await account.click();
    const menu = page.locator('[role="menu"]');
    await menu.waitFor();
    await expect(menu.locator('[role="menuitem"]')).toHaveText(['Settings', 'Log out']);
    const glyphs = await menu.locator('[role="menuitem"]').evaluateAll((items) =>
      items.map((item) => {
        const glyph = item.querySelector('svg')?.getBoundingClientRect();
        const label = item.querySelector('span')?.getBoundingClientRect();
        if (glyph === undefined || label === undefined) return null;
        return {
          href: item.querySelector('svg use')?.getAttribute('href'),
          size: [glyph.width, glyph.height],
          before: glyph.right <= label.left,
          offCentre: Math.abs((glyph.top + glyph.bottom) / 2 - (label.top + label.bottom) / 2),
        };
      }),
    );
    expect(glyphs.map((one) => one?.href), 'an item draws no glyph').toEqual(['#i-settings', '#i-logout']);
    for (const one of glyphs) {
      expect(one?.size, 'a glyph is not the console\'s 15px icon').toEqual([15, 15]);
      expect(one?.before, 'a glyph is not before its label').toBe(true);
      expect(one?.offCentre, `a glyph sits ${px(one?.offCentre ?? 0)} off its label's middle`).toBeLessThanOrEqual(1);
    }
    const opened = await page.evaluate(() => {
      const panel = document.querySelector('[role="menu"]')?.getBoundingClientRect();
      const trigger = document.querySelector('#topbar button[aria-haspopup="menu"]')?.getBoundingClientRect();
      if (panel === undefined || trigger === undefined) throw new Error('no menu or no trigger');
      return { panel: { left: panel.left, right: panel.right, top: panel.top }, trigger: { right: trigger.right, bottom: trigger.bottom } };
    });
    expect(opened.panel.left, `the menu starts at ${px(opened.panel.left)}, left of the window`).toBeGreaterThanOrEqual(0);
    expect(opened.panel.right, `the menu ends at ${px(opened.panel.right)}, past the window`).toBeLessThanOrEqual(width);
    expect(Math.abs(opened.panel.right - opened.trigger.right), 'the menu is not under the name').toBeLessThanOrEqual(0.5);
    expect(opened.panel.top, 'the menu covers the name').toBeGreaterThanOrEqual(opened.trigger.bottom);
    await page.keyboard.press('Escape');
    await expect(menu).toHaveCount(0);
    await expect(account).toBeFocused();

    // The column folds and comes back by the corner's button, and by Ctrl+B.
    if (width >= 640) {
      await page.locator('#page').click({ position: { x: 5, y: 5 } });
      await page.keyboard.press('Control+b');
      await expect(page.locator('#menu')).toBeHidden();
    }
    // Folded, no column stays for it: the page has the window's whole width,
    // and the button is at the bottom left, over the page and covering no cell.
    if (width < 640 || !(await page.locator('#menu').isVisible())) {
      const sides = await page.evaluate(() => {
        const button = document.querySelector('button[data-kinds-at="corner"]')?.getBoundingClientRect();
        const cells = [...document.querySelectorAll('#page td')].map((cell) => cell.getBoundingClientRect());
        return {
          strip: document.querySelector('[data-kinds-strip]') !== null,
          page: document.getElementById('page')?.getBoundingClientRect().left ?? -1,
          left: button?.left ?? -1,
          fromFoot: innerHeight - (button?.bottom ?? 0),
          under:
            button === undefined
              ? -1
              : cells.filter(
                  (cell) =>
                    cell.left < button.right && cell.right > button.left && cell.top < button.bottom && cell.bottom > button.top,
                ).length,
        };
      });
      expect(sides.strip, 'a strip is drawn for the Kinds button').toBe(false);
      expect(sides.page, 'the page does not start at the window\'s edge').toBe(0);
      expect(sides.left, 'Kinds is not at the left edge').toBeLessThan(16);
      expect(sides.fromFoot, 'Kinds is not at the bottom').toBeLessThan(16);
      expect(sides.under, 'Kinds covers a cell').toBe(0);
    }
    await page.locator('button[data-kinds-at="corner"]').click();
    await expect(page.locator('#menu')).toBeVisible();
  });
}

/**
 * The kinds column folded with no rail: no strip stays for its button, so the
 * list has the window's whole width, and the button covers no cell even where
 * the rows fill the panel, on a list with a "+" and on one with none.
 */
for (const [width, scene] of [
  [390, 'one-cluster-pods-create'],
  [390, 'one-cluster-pods'],
  [900, 'one-cluster-pods-create'],
  [900, 'one-cluster-pods'],
] as const) {
  test(`at ${String(width)} the folded column leaves no strip and Kinds covers no cell, ${scene}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width < 600 ? 360 : 260 });
    await page.goto(`/?scene=${scene}`);
    const button = page.locator('button[data-kinds-at="corner"]');
    await page.locator('#panel td').first().waitFor();
    if (await page.locator('#menu').isVisible()) {
      await page.locator('#page').click({ position: { x: 5, y: 5 } });
      await page.keyboard.press('Control+b');
    }
    await expect(page.locator('#menu')).toBeHidden();
    await button.waitFor();

    const found = await page.evaluate(() => {
      const panel = document.getElementById('panel');
      const kinds = document.querySelector('button[data-kinds-at="corner"]')?.getBoundingClientRect();
      if (panel === null || kinds === undefined) throw new Error('no panel or no Kinds');
      const box = panel.getBoundingClientRect();
      const cells = [...panel.querySelectorAll('td')]
        .map((cell) => cell.getBoundingClientRect())
        .map((cell) => ({
          left: Math.max(cell.left, box.left),
          right: Math.min(cell.right, box.right),
          top: Math.max(cell.top, box.top),
          bottom: Math.min(cell.bottom, box.bottom),
        }))
        .filter((cell) => cell.right > cell.left && cell.bottom > cell.top);
      return {
        overflows: panel.scrollHeight > panel.clientHeight,
        strip: document.querySelector('[data-kinds-strip]') !== null,
        page: document.getElementById('page')?.getBoundingClientRect().left ?? -1,
        left: kinds.left,
        fromFoot: innerHeight - kinds.bottom,
        under: cells.filter(
          (cell) => cell.left < kinds.right && cell.right > kinds.left && cell.top < kinds.bottom && cell.bottom > kinds.top,
        ).length,
      };
    });
    expect(found.overflows, 'the rows do not fill the panel: the test measures nothing').toBe(true);
    expect(found.strip, 'a strip is drawn for the Kinds button').toBe(false);
    expect(found.page, "the page does not start at the window's edge").toBe(0);
    expect(found.left, 'Kinds is not at the left edge').toBeLessThan(16);
    expect(found.fromFoot, 'Kinds is not at the bottom').toBeLessThan(16);
    expect(found.under, 'Kinds covers a cell').toBe(0);
  });
}
