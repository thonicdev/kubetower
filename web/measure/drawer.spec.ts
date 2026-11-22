import { expect, test, type Page } from '@playwright/test';
import { px, verticalScrollers } from './layoutkit';

/* -------------------------------------------------------------------------- */
/* 23 — the drawer is widened from its inner edge                            */
/* -------------------------------------------------------------------------- */

/**
 * The drawer opened on a resource can be made wider, or narrower than its
 * 560px default.
 *
 * The numbers: at 1440 the rail and the kinds column leave 1192px to the list
 * and the drawer, and the drawer starts at 560. Dragged 200px left it is 760;
 * dragged as far as it goes it is 1192 - 305, because 305 is what the list has
 * at 1024 with the default drawer (`drawerWidth.ts`). The clamp on a narrower
 * window is the stylesheet's, so it is measured by resizing the window, not by
 * reading a handler.
 */
/** `DRAWER_FLOOR` in `ui/src/shell/drawerWidth.ts`: the narrowest drawer, as
 *  measured on a pod's controls row in this harness. */
const DRAWER_FLOOR = 444;

async function drawerAndList(page: Page): Promise<{ drawer: number; edge: number; list: number; sideways: number }> {
  return page.evaluate(() => {
    const drawer = document.getElementById('drawer');
    const list = drawer?.previousElementSibling;
    if (drawer === null || !(list instanceof HTMLElement)) throw new Error('no drawer or list');
    const box = drawer.getBoundingClientRect();
    return {
      drawer: box.width,
      edge: box.left,
      list: list.getBoundingClientRect().width,
      sideways: document.documentElement.scrollWidth - document.documentElement.clientWidth,
    };
  });
}

/** Drag the handle by `dx`, in steps, as a hand would. */
async function dragHandle(page: Page, dx: number): Promise<void> {
  const box = await page.getByRole('separator', { name: 'Resize the panel' }).boundingBox();
  if (box === null) throw new Error('the handle is not drawn');
  const x = box.x + box.width / 2;
  const y = box.y + box.height / 2;
  await page.mouse.move(x, y);
  await page.mouse.down();
  await page.mouse.move(x + dx, y, { steps: 8 });
  await page.mouse.up();
}

test('the drawer widens from its inner edge, keeps the list its minimum, and remembers the width', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto('/?scene=console-pods-open');
  await page.locator('#drawer:not([hidden])').waitFor();
  await page.getByRole('button', { name: 'More table options' }).waitFor();
  const handle = page.getByRole('separator', { name: 'Resize the panel' });

  const before = await drawerAndList(page);
  expect(before.drawer, 'the default drawer moved').toBeCloseTo(560, 0);
  expect(before.list, 'the default list moved').toBeCloseTo(632, 0);

  // Drawn on hover only: the border is the edge the rest of the time.
  const bar = (): Promise<string> =>
    handle.evaluate((element) => getComputedStyle(element, '::before').backgroundColor);
  await page.mouse.move(300, 300);
  const quiet = await bar();
  await handle.hover();
  const shown = await bar();
  console.log(`the handle's bar: ${quiet} at rest, ${shown} on hover`);
  expect(quiet, 'the handle is drawn at rest').toBe('rgba(0, 0, 0, 0)');
  expect(shown, 'the handle draws nothing on hover').not.toBe('rgba(0, 0, 0, 0)');

  await dragHandle(page, -200);
  const wider = await drawerAndList(page);
  console.log(`dragged 200px left at 1440: drawer ${px(wider.drawer)}, list ${px(wider.list)}`);
  expect(wider.drawer, 'the drawer did not follow the drag').toBeCloseTo(760, 0);
  expect(wider.list, 'the list did not give the drawer what it took').toBeCloseTo(432, 0);

  await dragHandle(page, -1000);
  const widest = await drawerAndList(page);
  console.log(`dragged as far as it goes at 1440: drawer ${px(widest.drawer)}, list ${px(widest.list)}`);
  expect(widest.drawer, 'the drawer went past what leaves the list its minimum').toBeCloseTo(1192 - 305, 0);
  expect(widest.list, 'the list lost its minimum').toBeGreaterThanOrEqual(305 - 0.5);
  expect(widest.sideways, 'the page scrolls sideways').toBeLessThanOrEqual(0);
  expect(await handle.getAttribute('aria-valuenow')).toBe(String(1192 - 305));
  expect(await handle.getAttribute('aria-valuemax')).toBe(String(1192 - 305));
  // The narrowest is what the pod's content measured, not the default.
  expect(Number(await handle.getAttribute('aria-valuemin'))).toBeGreaterThanOrEqual(DRAWER_FLOOR);
  expect(Number(await handle.getAttribute('aria-valuemin'))).toBeLessThan(560);

  // The list's toolbar is whole beside the widest drawer, as at 1024 (test 19).
  const toolbar = await page.evaluate(() => {
    const header = document.querySelector('main header');
    if (!(header instanceof HTMLElement)) throw new Error('no toolbar');
    const right = Math.max(
      ...[...header.querySelectorAll<HTMLElement>('button, input')].map((one) => one.getBoundingClientRect().right),
    );
    return { right, scroll: header.scrollWidth, client: header.clientWidth };
  });
  expect(toolbar.right, 'a toolbar control is under the drawer').toBeLessThanOrEqual(widest.edge + 0.5);
  expect(toolbar.scroll, 'the toolbar is wider than its box').toBeLessThanOrEqual(toolbar.client);

  // Test 21's rule still holds: the document scrolls nowhere.
  const scrolling = await verticalScrollers(page);
  expect(scrolling.document, 'the document scrolls under a widened drawer').toBeLessThanOrEqual(scrolling.window);
  for (const scroller of scrolling.scrollers) {
    expect(['#panel'], `${scroller} scrolls under a widened drawer`).toContain(scroller);
  }

  // A reload keeps it.
  await page.reload();
  await page.locator('#drawer:not([hidden])').waitFor();
  await page.getByRole('button', { name: 'More table options' }).waitFor();
  expect((await drawerAndList(page)).drawer, 'the width did not survive a reload').toBeCloseTo(1192 - 305, 0);

  // A narrower window takes it back towards the default before the list gives
  // anything, and the default is where it stops: 1024 is drawn as today.
  for (const [width, drawer, list] of [
    [1280, 1280 - 248 - 305, 305],
    [1024, 471.04, 304.96],
  ] as const) {
    await page.setViewportSize({ width, height: 900 });
    const at = await drawerAndList(page);
    console.log(`the remembered width at ${String(width)}: drawer ${px(at.drawer)}, list ${px(at.list)}`);
    expect(at.drawer, `at ${String(width)} the drawer is not clamped`).toBeCloseTo(drawer, 0);
    expect(at.list, `at ${String(width)} the list is not what it should be`).toBeCloseTo(list, 0);
    expect(at.sideways, `at ${String(width)} the page scrolls sideways`).toBeLessThanOrEqual(0);
  }
  await page.setViewportSize({ width: 1440, height: 900 });
  expect((await drawerAndList(page)).drawer, 'the width did not come back with the room').toBeCloseTo(1192 - 305, 0);

  // Home, from the keyboard, is the default again.
  await handle.focus();
  await page.keyboard.press('Home');
  expect((await drawerAndList(page)).drawer, 'Home did not go back to the default').toBeCloseTo(560, 0);
});

test('the object page has no drawer, so no handle', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto('/?scene=console-pod-tall-page');
  await page.locator('#object-panel-details').waitFor();
  await expect(page.getByRole('separator', { name: 'Resize the panel' })).toHaveCount(0);
});

/**
 * Whether the drawer's header, controls row and tab strip each fit: nothing
 * outside its row, the kind label whole, the controls on one line (centers
 * within a pixel - the spacer is 0px tall) and no row wider than its box. The
 * object's name is left out: it truncates by design at every width.
 */
async function drawerFits(page: Page): Promise<string[]> {
  return page.evaluate(() => {
    const problems: string[] = [];
    const aside = document.getElementById('drawer');
    const header = aside?.querySelector('[data-object-chrome="drawer"] > header');
    const row = aside?.querySelector('[data-object-controls]');
    const tabs = aside?.querySelector('[role="tablist"]');
    if (!(header instanceof HTMLElement) || !(row instanceof HTMLElement) || !(tabs instanceof HTMLElement)) {
      return ['the drawer has no header, controls row or tab strip'];
    }
    const box = header.getBoundingClientRect();
    const controls = [...header.querySelectorAll<HTMLElement>('a, button')];
    for (const control of controls) {
      const at = control.getBoundingClientRect();
      if (at.left < box.left - 0.5 || at.right > box.right + 0.5) problems.push(`header: ${control.getAttribute('aria-label') ?? ''} is cut`);
    }
    const text = header.firstElementChild;
    const first = controls[0];
    if (text !== null && first !== undefined && text.getBoundingClientRect().right > first.getBoundingClientRect().left + 0.5) {
      problems.push('header: the name runs under the controls');
    }
    const kind = text?.querySelector('p');
    if (kind instanceof HTMLElement && kind.scrollWidth > kind.clientWidth + 0.5) problems.push('header: the kind is cut');
    const centres = new Set(
      [...row.children]
        .filter((child) => child.getBoundingClientRect().width > 0)
        .map((child) => {
          const at = child.getBoundingClientRect();
          return Math.round(at.top + at.height / 2);
        }),
    );
    if (centres.size > 1) problems.push(`controls: on ${String(centres.size)} lines`);
    if (row.scrollWidth > row.clientWidth + 0.5) problems.push('controls: wider than the row');
    if (tabs.scrollWidth > tabs.clientWidth + 0.5) problems.push('tabs: wider than the strip');
    const strip = tabs.getBoundingClientRect();
    for (const tab of tabs.querySelectorAll('button')) {
      const at = tab.getBoundingClientRect();
      if (at.right > strip.right + 0.5 || at.top > strip.top + 2) problems.push(`tabs: ${tab.textContent ?? ''} is cut`);
    }
    return problems;
  });
}

for (const kind of ['pods', 'services', 'secrets'] as const) {
  test(`the drawer narrows to what a ${kind.slice(0, -1)}'s header, controls and tabs need, and no further`, async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto(`/?scene=console-drawer-${kind}`);
    await page.locator('#drawer:not([hidden]) [role="tablist"] button').nth(2).waitFor();
    const handle = page.getByRole('separator', { name: 'Resize the panel' });

    expect(await drawerFits(page), 'the default drawer does not fit its own content').toStrictEqual([]);
    await dragHandle(page, 600);
    const at = await drawerAndList(page);
    const min = Number(await handle.getAttribute('aria-valuemin'));
    const need = await page.evaluate(() => document.getElementById('drawer')?.style.getPropertyValue('--drawer-need'));
    console.log(`a ${kind.slice(0, -1)}'s drawer at its narrowest at 1440: ${px(at.drawer)} (aria-valuemin ${String(min)}, --drawer-need ${need ?? ''}), list ${px(at.list)}`);
    expect(at.drawer, 'the drawer is not at the narrowest it says it may go').toBeCloseTo(min, 0);
    expect(min, 'the narrowest is under the measured floor').toBeGreaterThanOrEqual(DRAWER_FLOOR);
    expect(at.list, 'the list did not get the room back').toBeCloseTo(1192 - min, 0);
    expect(at.sideways, 'the page scrolls sideways').toBeLessThanOrEqual(0);
    expect(await drawerFits(page), `at ${px(at.drawer)} the drawer's content does not fit`).toStrictEqual([]);
    expect(await page.evaluate(() => localStorage.getItem('kubetower.drawer.width'))).toBe(String(min));

    if (kind === 'pods') {
      // The floor is the pod's own measurement: a pixel narrower and its
      // controls row no longer fits on one line.
      await page.evaluate((width) => {
        const aside = document.getElementById('drawer');
        if (aside === null) return;
        aside.style.minWidth = '0';
        aside.style.flexBasis = `${String(width)}px`;
      }, min - 1);
      expect(await drawerFits(page), 'a pixel under the floor still fits: the floor is not measured').toContain(
        'controls: on 2 lines',
      );
    }
  });
}

test('a width stored under the floor is drawn at the floor after a reload, beside the 1024px list', async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem('kubetower.drawer.width', '200');
  });
  for (const width of [1440, 1024]) {
    await page.setViewportSize({ width, height: 800 });
    await page.goto('/?scene=console-pods-open');
    await page.locator('#drawer:not([hidden])').waitFor();
    await page.getByRole('button', { name: 'More table options' }).waitFor();
    // The floor is measured on what the drawer holds, and the pod's verbs only
    // arrive with its payload - a request of its own, after the list's. Read
    // before them, the drawer stands at the 444px it needs without them and the
    // controls wrap once they land, which a loaded runner shows. So
    // wait for the verbs, then for the frame in which the drawer's observer
    // republishes `--drawer-need` after them: it was scheduled when they were
    // drawn, so one requested now runs after it.
    await page.locator('#drawer [data-object-controls] [data-arm-spacer]').waitFor({ state: 'attached' });
    await page.evaluate(
      () =>
        new Promise<void>((done) => {
          requestAnimationFrame(() => {
            done();
          });
        }),
    );
    expect(
      await page.evaluate(() => document.getElementById('drawer')?.style.getPropertyValue('--drawer-need') ?? ''),
      `at ${String(width)} the drawer published no need`,
    ).not.toBe('');
    const at = await drawerAndList(page);
    console.log(`a stored 200px at ${String(width)}: drawer ${px(at.drawer)}, list ${px(at.list)}`);
    // At 1440 and at 1024 alike the floor is under the default (560, 471).
    expect(at.drawer, `at ${String(width)} a stored 200px is not clamped up to the floor`).toBeGreaterThanOrEqual(DRAWER_FLOOR - 0.5);
    expect(at.drawer, `at ${String(width)} the drawer is wider than the default`).toBeLessThanOrEqual(Math.min(560, width * 0.46) + 0.5);
    expect(await drawerFits(page), `at ${String(width)} the narrowed drawer's content does not fit`).toStrictEqual([]);
    expect(at.sideways, `at ${String(width)} the page scrolls sideways`).toBeLessThanOrEqual(0);

    // The list's toolbar, as test 19 measures it under the default drawer.
    const toolbar = await page.evaluate(() => {
      const header = document.querySelector('main header');
      if (!(header instanceof HTMLElement)) throw new Error('no toolbar');
      const controls = [...header.querySelectorAll<HTMLElement>('button, input')];
      const search = controls.find((one) => (one.getAttribute('placeholder') ?? '').startsWith('search all columns'));
      return {
        right: Math.max(...controls.map((one) => one.getBoundingClientRect().right)),
        search: search?.getBoundingClientRect().width ?? 0,
        scroll: header.scrollWidth,
        client: header.clientWidth,
      };
    });
    expect(toolbar.right, `at ${String(width)} a toolbar control is under the drawer`).toBeLessThanOrEqual(at.edge + 0.5);
    expect(toolbar.search, `at ${String(width)} the search box is squeezed`).toBeGreaterThanOrEqual(96);
    expect(toolbar.scroll, `at ${String(width)} the toolbar is wider than its box`).toBeLessThanOrEqual(toolbar.client);
  }
});
