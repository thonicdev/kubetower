import { expect, test } from '@playwright/test';
import { px, show } from './layoutkit';

/* -------------------------------------------------------------------------- */
/* 13 — the dock's log toolbar is one row, and its ⋯ menu stays inside       */
/* -------------------------------------------------------------------------- */

/**
 * Time, JSON, Wrap and Download sit behind a `⋯`. In the `dock-log` scene,
 * with three containers and a filter typed, the bar is one row at 1024 px -
 * **asserted**, since a `flex-wrap` bar never scrolls sideways and a check of
 * that alone passes a bar of two rows. The width the four leave goes to the
 * filter, which is asserted wider than its own minimum. And *no horizontal
 * scrollbars*: neither the bar nor the page scrolls sideways.
 */
for (const width of [1024, 1280]) {
  test(`the dock's log toolbar is one row at ${String(width)}px, gives the filter the room, and never scrolls sideways`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 800 });
    await show(page, 'dock-log');
    const more = page.getByRole('button', { name: 'More log options' });
    await more.waitFor();
    await page.getByRole('button', { name: /prometheus/ }).first().waitFor();
    // A filter typed, so the match counter is drawn too.
    await page.getByLabel('Filter the log').fill('scrape');

    const bar = await more.evaluate((button) => {
      const toolbar = button.closest('.border-b');
      if (!(toolbar instanceof HTMLElement)) throw new Error('no toolbar around the ⋯');
      const middle = (box: DOMRect): number => box.top + box.height / 2;
      const line = middle(button.getBoundingClientRect());
      // Every drawn control's middle, against the ⋯'s: on one row they agree.
      const off = [...toolbar.children]
        .map((child) => child.getBoundingClientRect())
        .filter((box) => box.width > 0 && box.height > 0)
        .map((box) => Math.abs(middle(box) - line));
      const filter = toolbar.querySelector('input[type="search"]');
      if (!(filter instanceof HTMLElement)) throw new Error('no filter in the toolbar');
      // The room before the ⋯: from the drawn control before it, less the
      // bar's own gap, to the ⋯'s box. The auto margin, measured.
      const holder = [...toolbar.children].find((child) => child.contains(button));
      if (holder === undefined) throw new Error('the ⋯ is not a child of the toolbar');
      let before = holder.previousElementSibling;
      while (before !== null && before.getBoundingClientRect().width === 0) before = before.previousElementSibling;
      if (before === null) throw new Error('nothing drawn before the ⋯');
      const slack =
        holder.getBoundingClientRect().left -
        before.getBoundingClientRect().right -
        Number.parseFloat(getComputedStyle(toolbar).columnGap);
      return {
        scrollWidth: toolbar.scrollWidth,
        clientWidth: toolbar.clientWidth,
        width: toolbar.getBoundingClientRect().width,
        drift: Math.max(...off),
        filter: filter.getBoundingClientRect().width,
        filterMax: Number.parseFloat(getComputedStyle(filter).maxWidth),
        slack,
        right: button.getBoundingClientRect().right,
        page: document.documentElement.scrollWidth,
        viewport: window.innerWidth,
      };
    });
    console.log(
      `dock log toolbar at ${String(width)}px: ${px(bar.width)} wide, rows drift ${px(bar.drift)}, ` +
        `filter ${px(bar.filter)}, room before the ⋯ ${px(bar.slack)}`,
    );
    expect(bar.drift, 'the toolbar is more than one row').toBeLessThanOrEqual(2);
    // The filter took the room the four controls gave back: either it is as
    // wide as it may be, or there is no room left over before the ⋯. True on
    // any fonts, unlike a width threshold.
    expect(
      bar.filter >= bar.filterMax - 0.5 || bar.slack < 1,
      `the filter is ${px(bar.filter)} of ${px(bar.filterMax)} and ${px(bar.slack)} is left before the ⋯`,
    ).toBe(true);
    expect(bar.scrollWidth, 'the toolbar scrolls sideways').toBeLessThanOrEqual(bar.clientWidth);
    expect(bar.page, 'the page scrolls sideways').toBeLessThanOrEqual(bar.viewport);
    expect(bar.right, 'the ⋯ is past the window').toBeLessThanOrEqual(bar.viewport);

    // Shift+Tab out of the open menu lands on the control before the ⋯, not on
    // the first thing in the document.
    await more.focus();
    await page.keyboard.press('Enter');
    await expect(page.getByRole('menuitemcheckbox', { name: 'Time' })).toBeFocused();
    await page.keyboard.press('Shift+Tab');
    await expect(page.getByRole('menu')).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Next match' })).toBeFocused();
  });
}

/**
 * A bar too narrow for one row wraps, and the ⋯ stays at the right of the row
 * it lands on. At a 900 px window the dock is about 616 px and the bar wraps;
 * an auto margin on a zero-width spacer of its own would stay on the first row
 * and leave the ⋯ alone at the left of the second, so the margin is on the ⋯'s
 * own box.
 */
test("the log toolbar's ⋯ stays at the right edge when the bar wraps, at a 900px window", async ({ page }) => {
  await page.setViewportSize({ width: 900, height: 800 });
  await show(page, 'dock-log');
  const more = page.getByRole('button', { name: 'More log options' });
  await more.waitFor();
  await page.getByRole('button', { name: /prometheus/ }).first().waitFor();
  await page.getByLabel('Filter the log').fill('scrape');

  const edge = await more.evaluate((button) => {
    const toolbar = button.closest('.border-b');
    if (!(toolbar instanceof HTMLElement)) throw new Error('no toolbar around the ⋯');
    const style = getComputedStyle(toolbar);
    const inner =
      toolbar.getBoundingClientRect().right -
      Number.parseFloat(style.paddingRight) -
      Number.parseFloat(style.borderRightWidth);
    return {
      inner,
      right: button.getBoundingClientRect().right,
      tall: toolbar.getBoundingClientRect().height,
      row: button.getBoundingClientRect().height,
    };
  });
  console.log(
    `dock log toolbar at 900px: ⋯ right ${px(edge.right)}, bar's inner right ${px(edge.inner)}, bar ${px(edge.tall)} tall`,
  );
  expect(Math.abs(edge.inner - edge.right), "the ⋯ is not at the toolbar's right edge").toBeLessThanOrEqual(2);
});

/**
 * The menu opens upward, and this is what holds it there. At the dock's 120 px
 * minimum the pane under the bar is shorter than the four items, so a menu
 * opening downward runs off the bottom of the window. At
 * the dock's maximum - the window less 120 px - the bar is near the top of the
 * window, and an upward menu must still clear it. The height is stored before
 * the page loads, where `DockContext` reads it.
 */
for (const dock of [120, 680]) {
  test(`the log toolbar's menu is inside an 800px window with the dock at ${String(dock)}px`, async ({ page }) => {
    await page.addInitScript(`window.localStorage.setItem('kubetower.dock.height', '${String(dock)}')`);
    await page.setViewportSize({ width: 1024, height: 800 });
    await show(page, 'dock-log');
    const more = page.getByRole('button', { name: 'More log options' });
    await more.waitFor();
    const drawn = await page.locator('#dock').boundingBox();
    expect(Math.round(drawn?.height ?? 0), 'the stored dock height was not applied').toBe(dock);

    await more.click();
    for (const name of ['Time', 'JSON', 'Wrap', 'Download']) {
      const item = await page.getByRole(name === 'Download' ? 'menuitem' : 'menuitemcheckbox', { name }).boundingBox();
      if (item === null) throw new Error(`${name} is not drawn`);
      expect(item.y, `${name} is above the window`).toBeGreaterThanOrEqual(0);
      expect(item.y + item.height, `${name} is below the window`).toBeLessThanOrEqual(800);
      expect(item.x, `${name} is left of the window`).toBeGreaterThanOrEqual(0);
      expect(item.x + item.width, `${name} is right of the window`).toBeLessThanOrEqual(1024);
    }
  });
}

/* -------------------------------------------------------------------------- */
/* 20 — a selection in a following log pane copies                           */
/* -------------------------------------------------------------------------- */

/**
 * The copy itself, which jsdom cannot do (`LogSelection.test.tsx` holds the
 * rest). At about a thousand lines a second, a stream that slides the selected
 * rows out of the drawn window takes them out of the document, and a selection
 * copied three seconds after it was made would copy nothing.
 *
 * The stream is a stand-in `EventSource` driven from here, so the lines arrive
 * when the test says and not on a timer: more than `LOG_WINDOW` (2 000) of
 * them, after the selection and before the key. What is asserted is the
 * browser's own `copy` event and the selection at that moment, which is what
 * the clipboard receives - not the clipboard, whose permission is the
 * runner's business and not the pane's.
 */
test('a selection in a following log pane is what the copy shortcut copies, however many lines arrive', async ({
  page,
}) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.addInitScript(() => {
    type Source = { onmessage: ((event: { data: string }) => void) | null };
    const sources: Source[] = [];
    class StandIn {
      static readonly CONNECTING = 0;
      static readonly OPEN = 1;
      static readonly CLOSED = 2;
      readyState = 1;
      onmessage: ((event: { data: string }) => void) | null = null;
      onerror: (() => void) | null = null;
      constructor() {
        sources.push(this);
      }
      close(): void {
        this.readyState = 2;
      }
      addEventListener(): void {}
      removeEventListener(): void {}
    }
    const scope = window as unknown as Record<string, unknown>;
    scope.EventSource = StandIn;
    scope.__lines = (from: number, to: number): void => {
      const source = sources[sources.length - 1];
      for (let n = from; n < to; n++) {
        source?.onmessage?.({ data: JSON.stringify({ type: 'line', text: `line ${String(n)} ok` }) });
      }
    };
    const copies: string[] = [];
    scope.__copies = copies;
    document.addEventListener('copy', () => copies.push(String(document.getSelection())));
  });
  await show(page, 'dock-log');
  const lines = async (from: number, to: number): Promise<void> => {
    await page.evaluate(`window.__lines(${String(from)}, ${String(to)})`);
  };
  const copies = async (): Promise<string[]> => page.evaluate<string[]>('window.__copies');

  await page.getByRole('button', { name: 'More log options' }).waitFor();
  await lines(0, 40);
  const first = page.locator('[data-line]', { hasText: /^line 30 ok$/ });
  const last = page.locator('[data-line]', { hasText: /^line 33 ok$/ });
  await last.waitFor();

  // A drag, as a person makes one, over rows the following pane shows.
  const from = await first.boundingBox();
  const to = await last.boundingBox();
  if (from === null || to === null) throw new Error('the rows to select are not drawn');
  await page.mouse.move(from.x + 1, from.y + from.height / 2);
  await page.mouse.down();
  await page.mouse.move(to.x + 60, to.y + to.height / 2, { steps: 8 });
  await page.mouse.up();
  const chosen = await page.evaluate(() => String(document.getSelection()));
  expect(chosen, 'the drag selected nothing').toContain('line 31 ok');

  // A busy pod: more lines than the pane draws, between the selection and the key.
  await lines(40, 2_540);
  await expect(page.getByText(/Following is paused while text in this panel is selected/)).toBeVisible();

  await page.keyboard.press('ControlOrMeta+C');
  await expect.poll(copies, { message: 'the copy shortcut never reached the browser' }).toHaveLength(1);
  expect((await copies())[0], 'the copy took something other than the selection').toBe(chosen);

  // Let go, and the pane draws what it held back and follows again.
  const pane = await first.evaluate((row) => {
    const box = row.closest('pre')?.parentElement?.getBoundingClientRect();
    if (box === undefined) throw new Error('no pane around the rows');
    return { x: box.x, y: box.y };
  });
  await page.mouse.click(pane.x + 600, pane.y + 8);
  await expect(page.locator('[data-line]').last()).toHaveText('line 2539 ok');
  await expect(page.getByText(/Following is paused/)).toHaveCount(0);
});
