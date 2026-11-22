import { expect, test } from '@playwright/test';
import { px, show } from './layoutkit';

/**
 * The Overview's other wide tables fit their panels, with no sideways
 * scroll, at 1024, 1280, 1440 and 1920px: "Workloads not in the state they
 * declare", "Recent warning events" and "Containers that keep exiting", the
 * last with a 52-character pod name in the fixture. Each fits the way the node
 * table does: one column takes what the others leave - the trouble's *What*, the events' *Message*, the
 * restarting *Pod* - and the long names are cut at the end with the whole
 * value on hover.
 *
 * What the fit alone cannot see, and so is asserted beside it:
 *
 * - **Cut on screen, whole on hover.** Every cut value carries its full text
 *   in `title`, and the fixture's longest of each is cut at 1024 - a check
 *   that passed because nothing was long enough to cut would prove nothing.
 * - **The column that takes the rest keeps a readable share of it**, a
 *   quarter of the wrapper at least. `max-w-0` takes it out of the table's
 *   minimum width, so "no sideways scroll" holds by construction for anything
 *   beside it: a seventh column would squeeze it to nothing and the fit would
 *   stay green.
 * - **One line per row** in the trouble table, and in the events table for
 *   every cell but the message, which is a sentence and wraps on purpose
 *   (`.message`).
 * - **Every heading is padded as its cells are**: both tables take the node
 *   table's tighter gutters below `2xl`, for the room they give the rest.
 */
const OVERVIEW_TABLES = [
  {
    label: 'Workloads not in the state they declare',
    // The column that takes the rest, by heading.
    rest: 'What',
    // Every cut column by heading, with the fixture's longest value in it:
    // `cut` where that value is wider than its box at 1024px, `capped` where
    // the box is lifted at `xl` and the value must then be whole.
    cut: [
      { heading: 'Namespace', longest: 'search-platform-observability-eu-west-3', cut: true, capped: true },
      { heading: 'Name', longest: 'search-indexer-shard-rebalancer-eu-west-3', cut: true, capped: true },
      {
        heading: 'What',
        longest: 'rollout in progress — 4 of 6 updated, 1 pending on a node with memory pressure',
        cut: true,
        capped: false,
      },
    ],
    wraps: null,
  },
  {
    label: 'Recent warning events',
    rest: 'Message',
    cut: [
      { heading: 'Namespace', longest: 'reporting', cut: false, capped: true },
      { heading: 'Object', longest: 'Pod/ledger-api-7d9f8b6c4-x2k9p', cut: true, capped: true },
      { heading: 'Reason', longest: 'NodeHasInsufficientMemory', cut: true, capped: true },
    ],
    wraps: 'Message',
  },
  {
    // The same pattern on the same page: the pod takes
    // the rest, the namespace and the container are capped.
    label: 'Containers that keep exiting',
    rest: 'Pod',
    cut: [
      { heading: 'Namespace', longest: 'observability', cut: false, capped: true },
      {
        heading: 'Pod',
        longest: 'kube-prometheus-stack-prometheus-node-exporter-x2k9p',
        cut: true,
        capped: false,
      },
      { heading: 'Container', longest: 'wait-for-opensearch', cut: false, capped: true },
    ],
    wraps: null,
  },
] as const;

for (const width of [1024, 1280, 1440, 1920]) {
  for (const spec of OVERVIEW_TABLES) {
    test(`the overview's "${spec.label}" table fits at ${String(width)}px`, async ({ page }) => {
      await page.setViewportSize({ width, height: 900 });
      await page.goto('/?scene=overview');
      const table = page.locator(`table[aria-label="${spec.label}"]`);
      await table.locator('tbody tr').first().waitFor();
      const measured = await table.evaluate(
        (element, { rest, cut, wraps }) => {
          const wrapper = element.parentElement;
          if (wrapper === null) throw new Error('the table has no wrapper');
          const headings = [...element.querySelectorAll('thead th')].map((th) => (th.textContent ?? '').trim());
          const column = (heading: string): number => {
            const index = headings.indexOf(heading);
            if (index < 0) throw new Error(`no column headed ${heading}: ${headings.join(', ')}`);
            return index;
          };
          const rows = [...element.querySelectorAll('tbody tr')];

          // Each cut column: the element that carries the title, its shown and
          // full widths, on the row holding the fixture's longest value.
          const cuts = cut.map(({ heading, longest, cut: isCut, capped }) => {
            const index = column(heading);
            const cells = rows
              .map((row) => row.querySelectorAll('td')[index])
              .filter((cell): cell is HTMLTableCellElement => cell !== undefined);
            // A cell holding a value holds it in an element carrying its title;
            // an em dash is a value that was not there.
            const holders = cells
              .filter((cell) => (cell.textContent ?? '').trim() !== '—')
              // The init mark carries a title of its own, about the mark.
              .map((cell) => cell.querySelector('[title]:not([data-init-mark])'));
            const missing = holders.filter((el) => !(el instanceof HTMLElement)).length;
            const titled = holders.filter((el): el is HTMLElement => el instanceof HTMLElement);
            const untitled = titled
              .filter((el) => el.getAttribute('title') !== (el.textContent ?? '').trim())
              .map((el) => `${el.getAttribute('title') ?? '(none)'} for ${(el.textContent ?? '').trim()}`);
            const theLongest = titled.find((el) => el.getAttribute('title') === longest);
            return {
              heading,
              isCut,
              capped,
              missing,
              untitled,
              found: theLongest !== undefined,
              shown: theLongest?.clientWidth ?? 0,
              full: theLongest?.scrollWidth ?? 0,
            };
          });

          // Every text node on one line, except in the column that wraps.
          const wrapsAt = wraps === null ? -1 : column(wraps);
          const wrapped: string[] = [];
          for (const row of rows) {
            [...row.querySelectorAll('td')].forEach((cell, index) => {
              if (index === wrapsAt) return;
              const walker = document.createTreeWalker(cell, NodeFilter.SHOW_TEXT);
              for (let node = walker.nextNode(); node !== null; node = walker.nextNode()) {
                if ((node.textContent ?? '').trim() === '') continue;
                const range = document.createRange();
                range.selectNodeContents(node);
                const tops = new Set(
                  [...range.getClientRects()].filter((rect) => rect.width > 0).map((rect) => Math.round(rect.top)),
                );
                if (tops.size > 1) wrapped.push((node.textContent ?? '').trim());
              }
            });
          }

          const restCell = rows[0]?.querySelectorAll('td')[column(rest)];
          const firstCells = [...(rows[0]?.querySelectorAll('td') ?? [])];
          return {
            // Each heading's padding against its column's cells, as on the node
            // table: both tables pad tighter below `2xl` now.
            misaligned: [...element.querySelectorAll('thead th')].flatMap((th, index) => {
              const cell = firstCells[index];
              if (cell === undefined) return [`heading ${String(index)} has no cell`];
              const pad = (el: Element): string =>
                `${getComputedStyle(el).paddingLeft}/${getComputedStyle(el).paddingRight}`;
              return pad(th) === pad(cell) ? [] : [`${th.textContent ?? ''}: ${pad(th)} against ${pad(cell)}`];
            }),
            table: element.scrollWidth,
            wrapper: wrapper.clientWidth,
            scroll: wrapper.scrollWidth,
            rest: Math.round(restCell?.getBoundingClientRect().width ?? 0),
            columns: [...(rows[0]?.querySelectorAll('td') ?? [])].map((cell) =>
              Math.round(cell.getBoundingClientRect().width),
            ),
            cuts,
            wrapped,
          };
        },
        { rest: spec.rest, cut: spec.cut, wraps: spec.wraps },
      );
      // In the log, so CI's own font is on record beside the assertion.
      console.log(`"${spec.label}" at ${String(width)}px: ${JSON.stringify(measured)}`);

      expect(
        measured.scroll,
        `"${spec.label}" is ${String(measured.table)}px in a ${String(measured.wrapper)}px wrapper at ${String(width)}px`,
      ).toBeLessThanOrEqual(measured.wrapper);
      expect(
        measured.rest,
        `"${spec.rest}" is ${String(measured.rest)}px of a ${String(measured.wrapper)}px wrapper at ${String(width)}px; ` +
          'the column that takes the rest keeps a quarter of it',
      ).toBeGreaterThanOrEqual(Math.floor(measured.wrapper / 4));
      for (const c of measured.cuts) {
        expect(c.missing, `${c.heading}: rows with no titled value at ${String(width)}px`).toBe(0);
        expect(c.untitled, `${c.heading}: a title that is not the whole value at ${String(width)}px`).toEqual([]);
        expect(c.found, `${c.heading}: the fixture's longest value is not drawn`).toBe(true);
        if (width === 1024 && c.isCut) {
          // The fixture's longest is wider than its box here, so the ellipsis
          // path is the one measured rather than a fit nothing was cut for.
          expect(c.shown, `${c.heading}: the longest value is whole at 1024px, so nothing was cut`).toBeLessThan(c.full);
        }
        if (width >= 1280 && c.capped) {
          expect(
            c.shown,
            `${c.heading}: the longest value shows ${String(c.shown)}px of ${String(c.full)}px at ${String(width)}px, ` +
              'where the cut is lifted',
          ).toBeGreaterThanOrEqual(c.full);
        }
      }
      expect(measured.wrapped, `text on two lines in "${spec.label}" at ${String(width)}px`).toEqual([]);
      expect(measured.misaligned, `headings padded unlike their cells at ${String(width)}px`).toEqual([]);
    });
  }
}

/* -------------------------------------------------------------------------- */
/* 14 — an unmeasured `used` stays inside its tile                           */
/* -------------------------------------------------------------------------- */

/**
 * The dashboard of a cluster with **no metrics backend at all**, at 1280px.
 * Every `used` under a capacity ring is then a reading the server could not
 * take, and it arrives carrying its sentence — the longest of them a hundred
 * characters. jsdom lays nothing out, so `Panels.test.tsx` cannot see where
 * that sentence ends; this measures it.
 *
 * The number is each descendant's box against its tile's, with half a pixel for
 * sub-pixel rounding: nothing the tile draws may start before its left edge or
 * end past its right one.
 */
test('every capacity tile keeps its content inside it when nothing measured its usage, at 1280px', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 720 });
  await page.goto('/?scene=overview-unmeasured');
  await page.waitForSelector('[data-capacity]');

  const tiles = await page.locator('[data-capacity]').evaluateAll((nodes) =>
    nodes.map((tile) => {
      const box = tile.getBoundingClientRect();
      let worst = { past: 0, what: '' };
      for (const inner of Array.from(tile.querySelectorAll('*'))) {
        const own = inner.getBoundingClientRect();
        if (own.width === 0 && own.height === 0) continue;
        const past = Math.max(own.right - box.right, box.left - own.left);
        if (past > worst.past) worst = { past, what: (inner.textContent ?? inner.tagName).slice(0, 60) };
      }
      return { label: (tile as HTMLElement).dataset.capacity ?? '?', width: box.width, ...worst };
    }),
  );

  expect(tiles.length, 'three capacity tiles: CPU, Memory, Pods').toBe(3);
  for (const tile of tiles) {
    expect(
      tile.past,
      `${tile.label}: "${tile.what}" runs ${px(tile.past)} past its ${px(tile.width)} tile`,
    ).toBeLessThanOrEqual(0.5);
  }
});

/**
 * The right-click menu sits on a scrim, and Tab cycles inside it. The one way
 * out: with a save hung, a control that disables itself drops focus to the
 * body in Chromium, and Tab from the body walks behind the scrim. jsdom keeps focus on a disabled button, so only a real
 * engine shows it.
 *
 * The nameplate is written from two places. **The
 * menu**: a color is written at once and the menu stays, so with the POST
 * hung twelve Tabs and twelve Shift+Tabs (more than its stops) never leave it,
 * Reset says pending without leaving the order, and Escape still closes it.
 * **The Customize dialog**: Save keeps the keyboard and says pending while the
 * POST hangs, and Escape still closes the dialog - the console's `Dialog`,
 * which by design traps nothing, so only those two are asserted there.
 */
test('the tile menu and its dialog keep the keyboard while a save is pending, and Escape still closes them', async ({
  page,
}) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto('/?scene=names&at=%2F');
  const tile = page.locator('#rail a[data-cluster]').first();
  await tile.waitFor();
  // The save that never lands: the scene answers from `window.fetch`, so the
  // hang goes there too.
  await page.evaluate(() => {
    const answered = window.fetch.bind(window);
    window.fetch = (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
      if (init?.method === 'POST' && url.includes('/api/preferences/rail/nameplate')) {
        document.body.dataset['nameplatePosted'] = String(Number(document.body.dataset['nameplatePosted'] ?? '0') + 1);
        return new Promise<Response>(() => undefined);
      }
      return answered(input, init);
    };
  });

  // The menu: a color, written at once, with the POST hung.
  await tile.click({ button: 'right' });
  const menu = page.locator('[data-editor]');
  await menu.waitFor();
  await menu.locator('[role="radiogroup"] [role="radio"]').nth(4).click();
  await page.waitForFunction(() => document.body.dataset['nameplatePosted'] === '1');
  await page.waitForTimeout(100);

  const inside = (selector: string): Promise<{ inside: boolean; at: string }> =>
    page.evaluate((within) => {
      const box = document.querySelector(within);
      const at = document.activeElement;
      return {
        inside: box !== null && at !== null && box.contains(at),
        at: at === null ? 'nothing' : `${at.tagName} ${at.getAttribute('aria-label') ?? at.textContent ?? ''}`.trim(),
      };
    }, selector);
  expect((await inside('[data-editor]')).inside, 'the menu lost the keyboard when the save started').toBe(true);
  for (const key of ['Tab', 'Shift+Tab']) {
    for (let press = 1; press <= 12; press += 1) {
      await page.keyboard.press(key);
      const where = await inside('[data-editor]');
      expect(where.inside, `${key} ${String(press)} left the menu for ${where.at}`).toBe(true);
    }
  }
  // Pending is said to a reader, without taking Reset out of the order.
  await expect(menu.getByRole('button', { name: 'Reset icon' })).toHaveAttribute('aria-disabled', 'true');
  await page.keyboard.press('Escape');
  await expect(menu).toHaveCount(0);

  // The dialog: a style picked and saved, with the POST hung.
  await tile.click({ button: 'right' });
  await menu.waitFor();
  await menu.getByRole('button', { name: 'Customize icon…' }).click();
  const dialog = page.getByRole('dialog', { name: /^Icon for / });
  await dialog.waitFor();
  await dialog.locator('[data-icon-style="kt-pod"]').click();
  const save = dialog.getByRole('button', { name: 'Save' });
  await save.focus();
  await page.keyboard.press('Enter');
  await page.waitForFunction(() => document.body.dataset['nameplatePosted'] === '2');
  await page.waitForTimeout(100);
  expect(
    await save.evaluate((button) => button === document.activeElement),
    'Save lost the keyboard when the save started',
  ).toBe(true);
  await expect(save).toHaveAttribute('aria-disabled', 'true');
  await page.keyboard.press('Escape');
  await expect(dialog).toHaveCount(0);
});

/* -------------------------------------------------------------------------- */
/* 22 — a metrics source with nothing to say draws nothing                   */
/* -------------------------------------------------------------------------- */

/**
 * An Overview with a Prometheus that answers
 * and holds neither container nor pod cgroup cAdvisor series, no
 * metrics-server, and a cluster the server judged well. `overview-quiet` carries
 * that payload's own `history`, `top` and `allWell`. In a real browser, at
 * 1440px: no *Over time* section, no *Most
 * used* panel, no all-well pill - and the head still ends on the Capacity
 * rings, with the nodes pill beside the name.
 */
test('the quiet overview draws no history, no leaderboard and no all-well pill, at 1440px', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto('/?scene=overview-quiet');
  await page.waitForSelector('table[aria-label="Recent warning events"] tbody tr');

  const headings = await page.locator('#page h2').allTextContents();
  expect(headings.map((text) => text.trim())).not.toContain('Over time');
  expect(headings.map((text) => text.trim())).not.toContain('Most used');
  expect(headings.map((text) => text.trim())).toContain('Capacity');

  const text = (await page.locator('#page').textContent()) ?? '';
  for (const filler of ['Nothing to chart', 'Nothing here', 'not a claim', 'nothing needs acting on', 'no pod cgroup']) {
    expect(text, `the page still says "${filler}"`).not.toContain(filler);
  }
  await expect(page.locator('#page a', { hasText: '3 of 3 nodes ready' })).toBeVisible();
});

/**
 * A tile that opens stretches its open control over itself, so a click anywhere
 * opens it. The chips and the detail sit above that box: their hover sentences
 * are information (an Argo CD condition, where a Redis reading came from), so
 * the pointer must land on them and not on the link.
 */
test("a tile's chips answer the pointer, and a click on them still opens the tile", async ({ page }) => {
  await show(page, 'tiles');
  const chip = page.locator('[data-tile-second] [data-pill]');
  const detail = page.locator('[data-tile-detail] [title]');
  for (const target of [chip, detail]) {
    const box = await target.boundingBox();
    expect(box).not.toBeNull();
    if (box === null) return;
    const hit = await page.evaluate(
      ([x, y]) => {
        const element = document.elementFromPoint(x ?? 0, y ?? 0);
        return element?.closest('[title]')?.getAttribute('title') ?? null;
      },
      [box.x + box.width / 2, box.y + box.height / 2],
    );
    expect(hit).toBe(await target.getAttribute('title'));
    // Playwright's own check: no other element intercepts the pointer.
    await target.hover({ timeout: 2_000 });
  }
  const opened = page.locator('[data-opened]');
  await chip.click({ timeout: 2_000 });
  await expect(opened).toHaveAttribute('data-opened', '1');
  await page.getByRole('button', { name: 'cilium' }).click();
  await expect(opened).toHaveAttribute('data-opened', '2');
});
