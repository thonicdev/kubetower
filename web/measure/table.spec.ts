import { expect, test, type Page } from '@playwright/test';
import { px, show, sum, table } from './layoutkit';

test('a table fills its panel whether or not a column is stored, and a stored width is drawn at its share of it', async ({
  page,
}) => {
  await page.setViewportSize({ width: 1920, height: 900 });

  // Half one: nothing stored. `#panel > .table-scroll > .kt-grid:not(.sized)`
  // carries `min-width: 100%`, so the table fills the panel and the surplus is
  // shared out. A panel of 1290 has no padding, so all 1290 is content box.
  await show(page, 'list-unsized');
  const unsized = await table(page);

  expect(unsized.classes, 'an unsized table must not carry .sized').not.toContain('sized');
  expect(
    Math.abs(unsized.width - unsized.panel),
    `an unsized table must fill its panel: drawn ${px(unsized.width)} in ${px(unsized.panel)} of panel`,
  ).toBeLessThanOrEqual(1);

  /*
    Half two. **Some columns stored and some not** — the state the console is
    actually in after one drag. A stored width is a share of the panel, so a
    sized table widens with its window. Two things are asserted:

      - every column is drawn at the SAME multiple of what it declares, and that
        multiple is the panel over the sum of the declarations. A rule that
        beats the table's own sizing (`max-content` beaten by `w-full` or an id
        selector) draws columns at varying factors, refused here column by
        column;
      - the table fills the panel, AT TWO PANEL WIDTHS, which tells "fills its
        panel" from "happens to be that wide".
  */
  for (const width of [1290, 1700]) {
    await show(page, `list&width=${String(width)}`);
    const sized = await table(page);
    const declared = sum(sized.columns.map((column) => column.declared));
    const factor = sized.panel / declared;

    expect(sized.classes, 'one stored width puts the whole table into .sized').toContain('sized');
    expect(
      sized.classes,
      'a sized table must not carry `w-full`: @layer utilities beats @layer components whatever the specificity',
    ).not.toContain('w-full');

    /*
      The three stored widths are the fixture's own — 200, 56 and 90, 56 being
      the grip's floor — and the colgroup carries each one as its SHARE of the
      set rather than as its pixels, so what is asserted is the
      relation between two of them. It is the same statement in a unit that does
      not depend on the panel: Name declares 200 against Schedule's 56, so
      whatever those are written as, one is 3.571 times the other.

      Reading the absolute number back would only restate the unit, and the unit
      is what stops a stored arrangement overflowing its panel.
    */
    const declares = (label: string): number =>
      sized.columns.find((column) => column.label === label)?.declared ?? 0;
    for (const [label, want] of Object.entries({ Schedule: 200 / 56, 'Last run': 200 / 90 })) {
      expect(declares(label), `no column headed ${label}`).toBeGreaterThan(0);
      expect(
        Math.abs(declares('Name') / declares(label) - want),
        `Name declares ${String(declares('Name'))} and ${label} ${String(declares(label))}: ` +
          `${String(Math.round((declares('Name') / declares(label)) * 1000) / 1000)} against ${String(Math.round(want * 1000) / 1000)}`,
      ).toBeLessThanOrEqual(0.01);
    }

    expect(
      Math.abs(sized.width - sized.panel),
      `a sized table fills its panel: drawn ${px(sized.width)} in ${px(sized.panel)} at a panel of ${String(width)}`,
    ).toBeLessThanOrEqual(1);

    const drawn = sum(sized.columns.map((column) => column.drawn));
    expect(
      Math.abs(sized.width - drawn),
      `a table is the sum of its columns: ${px(sized.width)} drawn against ${px(drawn)} summed`,
    ).toBeLessThanOrEqual(1.5);

    for (const column of sized.columns) {
      expect(
        Math.abs(column.drawn - column.declared * factor),
        `${column.label} declares ${px(column.declared)} of ${px(declared)}, so in a ${px(sized.panel)} panel it is ` +
          `${px(column.declared * factor)} — drawn at ${px(column.drawn)}, a factor of ` +
          `${String(Math.round((column.drawn / column.declared) * 100) / 100)}x against ${String(Math.round(factor * 100) / 100)}x`,
      ).toBeLessThanOrEqual(1);
    }
  }

  /*
    Half three: **the panel is narrower than the arrangement, and that is the
    same case as any other.** A colgroup in pixels is only ever scaled UP,
    because CSS 2.1 §17.5.2.1 makes a table's width the greater of its own and
    the sum of its columns, so an arrangement summing past the panel would be
    drawn literally and scroll sideways.

    A share written as a percentage has no such asymmetry, so the table fills a
    700px panel exactly as it fills a 1 700px one and every column keeps the
    fraction it was dragged to. The 56px floor is on the gesture, not on
    screen: the table shrinks with its panel rather than growing a scrollbar.
  */
  for (const width of [700, 1290, 1700]) {
    await show(page, `list&width=${String(width)}`);
    const narrow = await table(page);
    const shares = sum(narrow.columns.map((column) => column.declared));

    expect(
      Math.abs(narrow.width - narrow.panel),
      `at a panel of ${px(narrow.panel)} the table is still exactly its panel: ${px(narrow.width)}`,
    ).toBeLessThanOrEqual(1);

    for (const column of narrow.columns) {
      const want = (column.declared / shares) * narrow.panel;
      expect(
        Math.abs(column.drawn - want),
        `${column.label} declares ${String(Math.round((column.declared / shares) * 1000) / 10)}% of the table, ` +
          `so ${px(want)} of a ${px(narrow.panel)} panel — drawn at ${px(column.drawn)}`,
      ).toBeLessThanOrEqual(1);
    }
  }
});

/* -------------------------------------------------------------------------- */
/* 8 — a drag moves a boundary, not the table's right edge                   */
/* -------------------------------------------------------------------------- */

/** The grip at the right of a heading, dragged by `by` pixels. */
async function dragColumn(page: Page, label: string, by: number): Promise<void> {
  const grip = page.locator(`[aria-label="Resize the ${label} column"]`);
  const box = await grip.boundingBox();
  if (box === null) throw new Error(`no grip for ${label}`);
  const y = box.y + box.height / 2;
  await page.mouse.move(box.x + box.width / 2, y);
  await page.mouse.down();
  await page.mouse.move(box.x + box.width / 2 + by, y, { steps: 8 });
  await page.mouse.up();
}

test('a drag takes the difference from the columns after it, and what it posts fills the panel again after a reload', async ({
  page,
}) => {
  await page.setViewportSize({ width: 1920, height: 900 });

  /*
    The `-live` scenes keep what the gesture posts, which is what `Store.Resize`
    does for the console — `ListScene` in `main.tsx` says what that stands in for
    and what it is a copy of. Without it the component reverts to its props on
    pointerup and there is nothing after a drag to measure.

    The unsized table, because that is the transition from unsized to sized and
    the one where every column changes at once.
  */
  await show(page, 'list-unsized-live');
  const before = await table(page);
  await dragColumn(page, 'Name', -120);
  const after = await table(page);

  const widthOf = (label: string, from: { columns: { label: string; drawn: number }[] }): number =>
    from.columns.find((column) => column.label === label)?.drawn ?? 0;
  const moved = widthOf('Name', after) - widthOf('Name', before);

  expect(
    Math.abs(after.width - after.panel),
    `a drag must not take the table off the right edge: ${px(after.width)} in ${px(after.panel)} of panel`,
  ).toBeLessThanOrEqual(1);
  expect(
    Math.abs(moved + 120),
    `Name was dragged 120px narrower and moved ${px(moved)}`,
  ).toBeLessThanOrEqual(2);
  expect(
    Math.abs(sum(after.columns.map((column) => column.drawn)) - sum(before.columns.map((column) => column.drawn))),
    'the columns after it absorbed exactly what it gave up, so the total is unchanged',
  ).toBeLessThanOrEqual(1.5);

  /*
    And the arrangement *between* the absorbing columns is unchanged: leaving
    them to share the difference equally would draw all seven at one width, Age
    as wide as Namespace.
  */
  const ratio = (from: { columns: { label: string; drawn: number }[] }): number =>
    widthOf('Namespace', from) / (widthOf('Age', from) || 1);
  expect(
    Math.abs(ratio(after) - ratio(before)),
    `Namespace against Age was ${String(Math.round(ratio(before) * 100) / 100)} and is ${String(Math.round(ratio(after) * 100) / 100)}`,
  ).toBeLessThanOrEqual(0.02);

  /*
    The round trip. What the gesture posted is what the store keeps and what the
    next payload carries back, so mounting fresh from exactly those numbers is a
    reload — and at a second panel width it is the half a rendering change cannot
    prove on its own: a share that survives the file still fills the window it
    comes back into.
  */
  const posted = await page.evaluate(() => {
    const held = document.querySelector('[data-posted]')?.getAttribute('data-posted') ?? '[]';
    return JSON.parse(held) as { key: string; width: number | null }[];
  });
  expect(posted.length, 'every column is posted, not only the one that was dragged').toBe(
    after.columns.length,
  );
  expect(posted[0]?.key, 'the dragged column is first').toBe('name');

  const stored = posted
    .filter((each) => each.width !== null)
    .map((each) => `${each.key}:${String(each.width)}`)
    .join(',');

  for (const width of [1290, 1700]) {
    await show(page, `list&width=${String(width)}&stored=${stored}`);
    const reloaded = await table(page);
    expect(
      Math.abs(reloaded.width - reloaded.panel),
      `reloaded at a panel of ${String(width)}: ${px(reloaded.width)} in ${px(reloaded.panel)}`,
    ).toBeLessThanOrEqual(1);

    // And the arrangement came back, not only the width: every column in the
    // same share of the table it was left in.
    for (const column of after.columns) {
      const now = widthOf(column.label, reloaded);
      expect(
        Math.abs(now / reloaded.width - column.drawn / after.width),
        `${column.label} held ${String(Math.round((column.drawn / after.width) * 1000) / 10)}% of the table and holds ` +
          `${String(Math.round((now / reloaded.width) * 1000) / 10)}% after the reload`,
      ).toBeLessThanOrEqual(0.005);
    }
  }

  /*
    The arrow keys, on a table that is already sized. It is the same
    redistribution and it is a separate path through the component - and the one
    a unit test cannot see at all, because jsdom lays nothing out, so `freeze()`
    there reads no rectangles and the arithmetic is never reached. One press of
    ArrowRight, the key that widens, must widen.
  */
  await show(page, 'list-unsized-live');
  await dragColumn(page, 'Name', -120);
  const beforeKey = await table(page);
  await page.locator('[aria-label="Resize the Name column"]').focus();
  await page.keyboard.press('ArrowRight');
  await page.waitForTimeout(200);
  const afterKey = await table(page);

  expect(
    widthOf('Name', afterKey) - widthOf('Name', beforeKey),
    `one press of ArrowRight is one 16px step wider: ${px(widthOf('Name', beforeKey))} to ${px(widthOf('Name', afterKey))}`,
  ).toBeGreaterThan(8);
  expect(
    Math.abs(afterKey.width - afterKey.panel),
    `and the table still fills its panel: ${px(afterKey.width)} in ${px(afterKey.panel)}`,
  ).toBeLessThanOrEqual(1);

});

/* -------------------------------------------------------------------------- */
/* 2 — the gauge track and its percentage share one line                     */
/* -------------------------------------------------------------------------- */

/**
 * The list at its edges: the headings stay where the rows scroll
 * under them, and an end-aligned heading stands over its values.
 *
 * Both are geometry jsdom does not have. `text-right` on the `<th>` does
 * nothing to the flex box the label sits in, so an end-aligned heading can
 * stand far from its values on a wide window; and a sticky heading is inert
 * when its scrollport is `.table-scroll`, the height of the whole table
 * (`DataGrid.tsx` records why).
 */
test('the headings of a long list stay at the top of the panel as it scrolls', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 900 });
  await show(page, 'list-tall');
  await page.waitForSelector('#panel tbody tr');

  const reading = await page.evaluate(() => {
    const panel = document.getElementById('panel');
    const head = document.querySelector('#panel thead th');
    if (panel === null || !(head instanceof HTMLElement)) throw new Error('no panel or heading');
    const before = head.getBoundingClientRect().top;
    panel.scrollTop = 400;
    return {
      scrolled: panel.scrollTop,
      panelTop: panel.getBoundingClientRect().top,
      before,
      after: head.getBoundingClientRect().top,
    };
  });

  expect(reading.scrolled, 'the scene must be long enough to scroll').toBeGreaterThan(300);
  expect(
    Math.abs(reading.after - reading.panelTop),
    `after scrolling ${px(reading.scrolled)} the heading is at ${px(reading.after)}, the panel's top at ${px(reading.panelTop)}`,
  ).toBeLessThanOrEqual(1);
});

test('an end-aligned heading stands over the right edge of its values', async ({ page }) => {
  for (const width of [900, 2552]) {
    await page.setViewportSize({ width, height: 900 });
    await show(page, `list-unsized&width=${String(width - 40)}`);
    await page.waitForSelector('#panel tbody tr');

    const columns = await page.evaluate(() => {
      const heads = [...document.querySelectorAll('#panel thead th')];
      const first = document.querySelector('#panel tbody tr');
      if (first === null) throw new Error('no row');
      const cells = [...first.querySelectorAll('td')];
      return heads
        .map((head, index) => {
          const cell = cells[index];
          const label = head.querySelector('span.truncate');
          if (cell === undefined || label === null) return null;
          const style = getComputedStyle(cell);
          return {
            name: (label.textContent ?? '').trim(),
            align: getComputedStyle(head).textAlign,
            labelRight: label.getBoundingClientRect().right,
            valueRight: cell.getBoundingClientRect().right - Number.parseFloat(style.paddingRight),
          };
        })
        .filter((one): one is NonNullable<typeof one> => one !== null && one.align === 'right');
    });

    expect(columns.length, 'the scene has end-aligned columns').toBeGreaterThan(0);
    for (const column of columns) {
      expect(
        Math.abs(column.valueRight - column.labelRight),
        `at ${String(width)}px ${column.name}'s heading ends at ${px(column.labelRight)} and its values at ${px(column.valueRight)}`,
      ).toBeLessThanOrEqual(1);
    }
  }
});

test('a gauge track sits beside its own percentage, inside its own cell', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 900 });
  await show(page, 'gauge');
  await page.waitForSelector('[data-gauged]');

  const reading = await page.evaluate(() => {
    // The text, not the box: a right-aligned numeric column's digits end a
    // padding short of the cell's edge, and 16px matters here.
    function textRect(element: Element): DOMRect {
      const range = document.createRange();
      range.selectNodeContents(element);
      return range.getBoundingClientRect();
    }

    function measureRow(row: number) {
      const cell = document.querySelector(`tbody tr:nth-child(${String(row)}) td:nth-child(4)`);
      const previous = document.querySelector(`tbody tr:nth-child(${String(row)}) td:nth-child(3)`);
      const track = cell?.querySelector('[data-gauged]');
      const digits = cell?.querySelector('[data-share]');
      if (
        !(cell instanceof HTMLElement) ||
        !(previous instanceof HTMLElement) ||
        !(track instanceof HTMLElement) ||
        !(digits instanceof HTMLElement)
      ) {
        throw new Error('the gauged cell, its neighbor, its track or its percentage is missing');
      }
      const style = getComputedStyle(cell);
      const box = cell.getBoundingClientRect();
      const trackBox = track.getBoundingClientRect();
      const digitsBox = textRect(digits);
      return {
        contentLeft: box.left + Number.parseFloat(style.paddingLeft),
        contentRight: box.right - Number.parseFloat(style.paddingRight),
        trackLeft: trackBox.left,
        trackRight: trackBox.right,
        trackMiddle: (trackBox.top + trackBox.bottom) / 2,
        trackWidth: trackBox.width,
        digitsLeft: digitsBox.left,
        digitsRight: digitsBox.right,
        digitsTop: digitsBox.top,
        digitsBottom: digitsBox.bottom,
        digitsText: digits.textContent ?? '',
        previousRight: textRect(previous).right,
      };
    }

    return { first: measureRow(1), second: measureRow(2) };
  });

  const one = reading.first;
  expect(one.digitsText).toBe('58%');

  // **Beside**, on one line. The track's middle is within
  // the percentage's line box.
  expect(
    one.trackMiddle >= one.digitsTop && one.trackMiddle <= one.digitsBottom,
    `the track's middle is at ${px(one.trackMiddle)} and its percentage spans ${px(one.digitsTop)} to ${px(one.digitsBottom)}`,
  ).toBe(true);

  // And after the track, with one gap between them: the bar ends where its own
  // figure begins.
  const toOwn = one.digitsLeft - one.trackRight;
  expect(toOwn, `the percentage starts ${px(toOwn)} after the track ends`).toBeGreaterThanOrEqual(0);
  expect(toOwn, `the percentage starts ${px(toOwn)} after the track ends`).toBeLessThanOrEqual(10);

  // The point of it in one line: the track must be nearer its own figure
  // than the previous column's digits, or the eye attaches it to that column.
  const toPrevious = one.trackLeft - one.previousRight;
  expect(
    toPrevious - toOwn,
    `the bar is ${px(toOwn)} from its own figure and ${px(toPrevious)} from the previous column's digits - it must be nearer its own`,
  ).toBeGreaterThan(0);

  // Inside its own cell, and a bar across it rather than a stub: the reserve
  // exists so the column is as wide as the line it draws.
  expect(one.trackLeft, 'the track starts inside its own cell').toBeGreaterThanOrEqual(one.contentLeft - 0.5);
  expect(one.trackWidth, `the track is drawn ${px(one.trackWidth)} wide`).toBeGreaterThan(40);
  // And the percentage ends inside it too: a figure past the right edge is drawn
  // over the next column, or clipped where the cell clips.
  for (const row of [one, reading.second]) {
    expect(
      row.digitsRight,
      `${row.digitsText} ends at ${px(row.digitsRight)}, its cell's content at ${px(row.contentRight)}`,
    ).toBeLessThanOrEqual(row.contentRight + 0.5);
  }

  // Part 4: the percentage's box is fixed, so `6%` leaves the bar exactly as
  // long as `58%` does - no arrangement of digits moves it.
  expect(reading.second.digitsText).toBe('6%');
  expect(
    Math.abs(reading.second.trackWidth - one.trackWidth),
    `the track is ${px(one.trackWidth)} beside 58% and ${px(reading.second.trackWidth)} beside 6%`,
  ).toBeLessThanOrEqual(0.5);
});

/** The fades the list draws over its panel, and the panel's visible edges. */
async function fades(
  page: Page,
): Promise<{ after: number | null; before: number | null; edge: number; left: number; held: number }> {
  return page.evaluate(() => {
    const panel = document.getElementById('panel');
    if (panel === null) throw new Error('no panel');
    const box = panel.getBoundingClientRect();
    return {
      after: document.querySelector('[data-sideways="after"]')?.getBoundingClientRect().right ?? null,
      before: document.querySelector('[data-sideways="before"]')?.getBoundingClientRect().left ?? null,
      // The fade keeps clear of the panel's own vertical scrollbar.
      edge: box.left + panel.clientLeft + panel.clientWidth,
      left: box.left,
      // The left fade starts where the held column ends, not at the panel's
      // edge: over the held column it would wash out the tick and the name.
      held: document.querySelector('#panel thead th[data-held]')?.getBoundingClientRect().right ?? box.left,
    };
  });
}

test('a table wider than its panel fades at the cut, and only while there is more', async ({ page }) => {
  await page.setViewportSize({ width: 1024, height: 800 });
  await page.goto('/?scene=console-pods');
  await page.locator('[data-row-menu]').first().waitFor();
  // A list fits its panel, so the overflow the fade is for is made
  // here: the table held at twice its panel.
  await page.locator('#panel table').evaluate((table) => {
    table.style.minWidth = '200%';
  });

  const wide = await page.evaluate(() => {
    const panel = document.getElementById('panel');
    return panel === null ? 0 : panel.scrollWidth - panel.clientWidth;
  });
  expect(wide, 'the table held wider does not overflow its panel, so nothing here is tested').toBeGreaterThan(50);

  const start = await fades(page);
  expect(start.after, 'no fade at the right of a table that goes on past it').not.toBeNull();
  expect(
    Math.abs((start.after ?? 0) - start.edge),
    `the fade ends at ${px(start.after ?? 0)}, the panel at ${px(start.edge)}`,
  ).toBeLessThanOrEqual(1);
  expect(start.before, 'a fade at the left of a table scrolled nowhere').toBeNull();

  await page.evaluate(() => {
    const panel = document.getElementById('panel');
    if (panel !== null) panel.scrollLeft = panel.scrollWidth;
  });
  await expect(page.locator('[data-sideways="after"]')).toHaveCount(0);
  const end = await fades(page);
  expect(end.before, 'no fade at the left once scrolled to the end').not.toBeNull();
  expect(end.held - end.left, 'no column is held at the left edge').toBeGreaterThan(50);
  expect(
    Math.abs((end.before ?? 0) - end.held),
    `the left fade starts at ${px(end.before ?? 0)}, the held column ends at ${px(end.held)}`,
  ).toBeLessThanOrEqual(1);

  // Where the table fits, nothing says there is more.
  await page.locator('#panel table').evaluate((table) => {
    table.style.minWidth = '';
  });
  await expect(page.locator('[data-sideways]')).toHaveCount(0);
});

test('a cut heading or cell offers the whole text on hover, and no pill is cut', async ({ page }) => {
  // Wide enough that every heading and the namespace are whole before the
  // columns below are narrowed by hand; at 1024 the list fits its panel by
  // cutting them.
  await page.setViewportSize({ width: 1920, height: 800 });
  await page.goto('/?scene=console-pods');
  await page.locator('[data-row-menu]').first().waitFor();

  const pills = await page.evaluate(() =>
    [...document.querySelectorAll<HTMLElement>('#panel tbody .rounded-full')]
      .filter((pill) => pill.scrollWidth > pill.clientWidth + 1)
      .map((pill) => `${pill.textContent ?? ''} (${String(pill.clientWidth)} of ${String(pill.scrollWidth)})`),
  );
  expect(pills, 'a pill is cut by a column sized for its text alone').toEqual([]);

  // No heading is cut by the widths the grid gives (`narrow.spec.ts`), so one
  // is cut here as a person's drag would: Containers' column narrowed under
  // its heading.
  await page.evaluate(() => {
    const at = [...document.querySelectorAll('#panel thead th')].findIndex((th) =>
      (th.textContent ?? '').trim().startsWith('Containers'),
    );
    const col = document.querySelectorAll<HTMLElement>('#panel colgroup col')[at];
    if (col !== undefined) col.style.width = '48px';
  });

  // Every heading, hovered: the cut ones offer their label, the whole ones nothing.
  const headings = page.locator('#panel thead th');
  const count = await headings.count();
  let cut = 0;
  for (let at = 0; at < count; at += 1) {
    const heading = headings.nth(at);
    await heading.scrollIntoViewIfNeeded();
    await heading.hover({ position: { x: 6, y: 6 } });
    const read = await heading.evaluate((th) => {
      const label = th.querySelector('[data-label]');
      return {
        label: label?.textContent ?? '',
        clipped: label instanceof HTMLElement && label.scrollWidth > label.clientWidth + 1,
        title: th.getAttribute('title'),
      };
    });
    if (read.clipped) {
      cut += 1;
      expect(read.title, `${read.label} is cut and offers no hover`).toBe(read.label);
    } else {
      expect(read.title, `${read.label} is whole and offers a hover anyway`).toBeNull();
    }
  }
  expect(cut, 'no heading is cut, so the hover is not tested').toBeGreaterThan(0);

  // A cell its column cuts, and one it does not: the Node column, narrowed
  // here until `kubeworker1` no longer fits, against the first column's name.
  const node = await page.evaluate(() => {
    const at = [...document.querySelectorAll('#panel thead th')].findIndex((th) =>
      (th.textContent ?? '').trim().startsWith('Node'),
    );
    const col = document.querySelectorAll<HTMLElement>('#panel colgroup col')[at];
    if (col !== undefined) col.style.width = '64px';
    return at;
  });
  const row = page.locator('#panel tbody tr').first();
  const cell = row.locator('td').nth(node);
  await cell.scrollIntoViewIfNeeded();
  await cell.hover();
  expect(await cell.getAttribute('title'), 'a cut Node cell offers no hover').toBe('kubeworker1');
  const whole = row.locator('td').nth(1);
  await whole.scrollIntoViewIfNeeded();
  await whole.hover();
  expect(await whole.getAttribute('title'), 'a whole namespace cell offers a hover anyway').toBeNull();
});

/**
 * A pill's text is semibold and costs more than
 * the grid's per-character figure, by more the longer it is. The longest
 * statuses a pod commonly reports, at 1024 - where every other column has
 * already given way so the list fits its panel, and the status holds - are whole.
 * The CI runner's font is the one that matters here (limit 4 in `main.tsx`).
 */
test('the longest common statuses are whole in their pills at 1024px', async ({ page }) => {
  await page.setViewportSize({ width: 1024, height: 800 });
  await page.goto('/?scene=console-pods-statuses');
  await page.getByText('CreateContainerConfigError').waitFor();
  const pills = await page.evaluate(() =>
    [...document.querySelectorAll<HTMLElement>('#panel tbody .rounded-full')].map((pill) => ({
      text: pill.textContent ?? '',
      shown: pill.clientWidth,
      whole: pill.scrollWidth,
    })),
  );
  const statuses = pills.map((pill) => pill.text);
  expect(statuses, 'the scene no longer draws the long statuses').toContain('CreateContainerConfigError');
  expect(
    pills.filter((pill) => pill.whole > pill.shown + 1).map((pill) => `${pill.text} (${String(pill.shown)} of ${String(pill.whole)})`),
    'a status is cut in its pill',
  ).toEqual([]);
});

/**
 * Every list is drawn at one size: the same heading, the same body text and the
 * same row height, whatever a row holds. A row holding a pill (Pods: Status)
 * and a row of plain text (Ingresses, Deployments, ClusterRoles) are the same
 * height; left to its content, the pill stood on the text's baseline and drew
 * the Pods list's rows 8px taller than every plain list's, which then read as
 * a smaller table.
 */
test('every list draws its headings, its text and its rows at one size, a pill or none', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  const drawn: Record<string, { head: number[]; rows: number[]; text: string[]; pills: string[] }> = {};
  for (const scene of ['console-pods', 'console-ingresses', 'console-deployments', 'console-clusterroles']) {
    await show(page, scene);
    await page.locator('#panel tbody tr td').first().waitFor();
    drawn[scene] = await page.evaluate(() => {
      const grid = document.querySelector('#panel .kt-grid');
      if (grid === null) throw new Error('no table under #panel');
      const height = (element: Element): number => Math.round(element.getBoundingClientRect().height * 10) / 10;
      return {
        head: [...grid.querySelectorAll('thead th')].map(height),
        rows: [...grid.querySelectorAll('tbody tr')].map(height),
        text: [...grid.querySelectorAll('tbody td')].map((cell) => getComputedStyle(cell).fontSize),
        pills: [...grid.querySelectorAll('tbody [data-pill]')].map((pill) => getComputedStyle(pill).verticalAlign),
      };
    });
  }
  const pods = drawn['console-pods'];
  if (pods === undefined) throw new Error('the Pods list was not drawn');
  expect(pods.pills.length, 'the Pods scene no longer draws a pill').toBeGreaterThan(0);
  expect(new Set(pods.pills), 'a pill stands on the baseline').toEqual(new Set(['middle']));
  const want = pods.rows[0] ?? 0;
  expect(want, 'no row drawn on Pods').toBeGreaterThan(0);
  for (const [scene, one] of Object.entries(drawn)) {
    console.log(`${scene}: rows ${[...new Set(one.rows)].join('/')}px, headings ${[...new Set(one.head)].join('/')}px`);
    expect(new Set(one.rows), `${scene}: rows drawn at heights other than the Pods list's ${px(want)}`).toEqual(
      new Set([want]),
    );
    expect(new Set(one.head), `${scene}: headings at another height than the Pods list's`).toEqual(new Set(pods.head));
    expect(new Set(one.text), `${scene}: body text at another size`).toEqual(new Set(['13px']));
  }
});
