import { expect, test } from '@playwright/test';
import { CONDITION_TYPES, show } from './layoutkit';

/* -------------------------------------------------------------------------- */
/* 24 — a fact's label holds one line, its value gives way                   */
/* -------------------------------------------------------------------------- */

/**
 * The drawer's fact tables, for a Pod, a Job, a CronJob, a Deployment and a
 * Terminating Namespace, at the drawer's width under a 390px, a 1024px, a
 * 1280px and a 1440px window: 179, 471, 560 and 560px of drawer
 * (`min(560px, 46vw)`), so 1280 is 1440's drawer beside a narrower list and
 * 1024 the one width where the cap cuts every condition type.
 *
 * A label cell carrying `wrap` lets a word break after any letter, so its
 * min-content width is one letter; a value with nowhere to break then squeezes
 * the label column to that, and `Kind` reads one letter per line. So, for every
 * row:
 *
 * - **the label is one line**, counted from its text boxes, never from the
 *   cell's height (a cell is as tall as its row);
 * - **the label is whole** - not cut - from 1024 for every label but a namespace
 *   condition type, and at 390 for the Summary's own four (`Kind`,
 *   `API version`, `Name`, `Namespace`); a label cut by the 40% cap keeps its
 *   whole text in its `title`;
 * - **nothing is drawn past the drawer's right edge**, and the drawer does not
 *   scroll sideways: the value is the column that wraps.
 *
 * At 390 the drawer is 179px and its table is under 16rem, so the rows stack -
 * label above value - and the same assertions hold of that layout: a
 * Namespace's `Terminating` badge beside a label cannot fit in 154px.
 */
for (const width of [390, 1024, 1280, 1440]) {
  for (const kind of ['pods', 'jobs', 'cronjobs', 'deployments', 'namespaces']) {
    test(`a ${kind} drawer's fact labels hold one line and its values stay inside it at ${String(width)}px`, async ({
      page,
    }) => {
      await page.setViewportSize({ width, height: 900 });
      await show(page, `facts&kind=${kind}`);
      await page.waitForSelector('[data-section="own"] tbody th');

      const measured = await page.evaluate(() => {
        const drawer = document.querySelector('[data-facts-drawer]');
        if (drawer === null) throw new Error('no drawer');
        const edge = drawer.getBoundingClientRect().right;
        const lines = (element: Element): number => {
          const half = Number.parseFloat(getComputedStyle(element).lineHeight) / 2;
          const tops: number[] = [];
          const walker = document.createTreeWalker(element, NodeFilter.SHOW_TEXT);
          for (let node = walker.nextNode(); node !== null; node = walker.nextNode()) {
            const range = document.createRange();
            range.selectNodeContents(node);
            for (const rect of range.getClientRects()) if (rect.width > 0) tops.push(rect.top);
          }
          tops.sort((a, b) => a - b);
          let count = 0;
          let start = Number.NEGATIVE_INFINITY;
          for (const top of tops) {
            if (top - start > half) {
              count += 1;
              start = top;
            }
          }
          return count;
        };
        const rows = [...drawer.querySelectorAll('[data-section] tbody tr')].map((tr) => {
          const th = tr.querySelector('th');
          if (th === null) throw new Error('a fact without its label');
          const label = (th.textContent ?? '').trim();
          const span = th.querySelector('[data-fact-label]');
          const right = Math.max(
            ...[tr, ...tr.querySelectorAll('*')].map((element) => element.getBoundingClientRect().right),
          );
          return {
            label,
            lines: lines(th),
            cut: span === null ? false : span.scrollWidth > span.clientWidth + 1,
            title: span?.getAttribute('title') ?? null,
            past: right - edge,
          };
        });
        return { rows, scroll: drawer.scrollWidth - drawer.clientWidth };
      });

      expect(measured.rows.length, 'no facts were drawn').toBeGreaterThan(4);
      const whole =
        width >= 1024
          ? measured.rows.filter((row) => !CONDITION_TYPES.includes(row.label))
          : measured.rows.filter((row) => ['Kind', 'API version', 'Name', 'Namespace'].includes(row.label));
      expect(
        measured.rows.filter((row) => row.lines !== 1).map((row) => `${row.label}: ${String(row.lines)} lines`),
        `labels not on one line at ${String(width)}px`,
      ).toEqual([]);
      expect(
        whole.filter((row) => row.cut).map((row) => row.label),
        `labels cut at ${String(width)}px`,
      ).toEqual([]);
      expect(
        measured.rows.filter((row) => row.title !== row.label).map((row) => row.label),
        'a label whose title is not its whole text',
      ).toEqual([]);
      expect(
        measured.rows.filter((row) => row.past > 0.5).map((row) => `${row.label}: ${row.past.toFixed(1)}px past`),
        `a fact drawn past the drawer's edge at ${String(width)}px`,
      ).toEqual([]);
      expect(measured.scroll, `the drawer scrolls sideways by ${String(measured.scroll)}px`).toBeLessThanOrEqual(0);
    });
  }
}

/**
 * A label the 40% cap cut is whole in its `title`, which only a mouse can
 * show; so it is also a button that opens it in place. At 1024 a Terminating
 * Namespace's four condition types are cut (a 471px drawer) and nothing else
 * is. Each of them, and nothing else, takes focus; Enter on one lays its whole
 * text out inside the cap, on as many lines as it needs, without widening the
 * label column past the cap or drawing anything past the drawer's edge; and
 * Enter again cuts it back to one line. The same with Space, which must not
 * scroll the drawer, and with a click.
 */
const OPENERS = ['Enter', 'Space', 'click'] as const;
for (const how of OPENERS) {
  test(`a cut fact label opens whole in place with ${how} and closes again`, async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 900 });
    await show(page, 'facts&kind=namespaces');
    await page.waitForSelector('[data-section="own"] tbody th');

    const buttons = page.locator('[data-fact-label][role="button"]');
    await expect(buttons).toHaveCount(CONDITION_TYPES.length);
    expect((await buttons.allTextContents()).sort()).toEqual([...CONDITION_TYPES].sort());
    expect(
      await page.locator('[data-fact-label]:not([role="button"])[tabindex]').count(),
      'an uncut label that takes focus',
    ).toBe(0);

    const label = buttons.filter({ hasText: 'NamespaceDeletionGroupVersionParsingFailure' });
    const read = () =>
      label.evaluate((span) => {
        const drawer = document.querySelector('[data-facts-drawer]');
        if (drawer === null) throw new Error('no drawer');
        const cell = span.closest('th');
        if (cell === null) throw new Error('a label outside its cell');
        const table = span.closest('table');
        if (table === null) throw new Error('a label outside its table');
        const lineHeight = Number.parseFloat(getComputedStyle(span).lineHeight);
        return {
          expanded: span.getAttribute('aria-expanded'),
          cut: span.scrollWidth > span.clientWidth + 1,
          lines: Math.round(span.getBoundingClientRect().height / lineHeight),
          share: cell.getBoundingClientRect().width / table.getBoundingClientRect().width,
          past: Math.max(...[...drawer.querySelectorAll('*')].map((one) => one.getBoundingClientRect().right)) -
            drawer.getBoundingClientRect().right,
          scroll: drawer.scrollWidth - drawer.clientWidth,
        };
      });

    const shut = await read();
    expect(shut).toMatchObject({ expanded: 'false', cut: true, lines: 1 });

    // Space on a `role="button"` that is not a <button> scrolls its scroller
    // unless its default is prevented. The drawer is given a height that
    // leaves its scroller room past the focused label, and that scroller's
    // position says whether Space moved it.
    const press = async (): Promise<void> => {
      if (how === 'click') await label.click();
      else await page.keyboard.press(how);
    };
    await page.locator('[data-facts-drawer]').evaluate((drawer) => {
      (drawer as HTMLElement).style.height = '160px';
    });
    await label.focus();
    // The label's own scroller: the nearest box above it that scrolls.
    const scrolled = () =>
      label.evaluate((span) => {
        let box = span.parentElement;
        while (box !== null && !/auto|scroll/.test(getComputedStyle(box).overflowY)) box = box.parentElement;
        if (box === null) throw new Error('the label has no scroller');
        return { top: box.scrollTop, room: box.scrollHeight - box.clientHeight - box.scrollTop };
      });
    const before = await scrolled();
    expect(before.room, 'the drawer has no room left to scroll, so Space is not tested').toBeGreaterThan(40);
    await press();
    const opened = await read();
    expect(opened.expanded).toBe('true');
    expect(opened.cut, 'the open label is still cut').toBe(false);
    expect(opened.lines, 'the open label is not laid out over lines').toBeGreaterThan(1);
    expect(opened.share, 'the open label widened its column past the cap').toBeLessThanOrEqual(0.45);
    expect(opened.past, 'a fact drawn past the drawer edge with the label open').toBeLessThanOrEqual(0.5);
    expect(opened.scroll).toBeLessThanOrEqual(0);
    await expect(label).toBeFocused();

    // A click is preceded by the runner scrolling the label into view, so only
    // a key's scroll position is the code's.
    if (how !== 'click') {
      expect((await scrolled()).top, `${how} scrolled the drawer when it opened the label`).toBe(before.top);
    }
    await press();
    expect(await read()).toMatchObject({ expanded: 'false', cut: true, lines: 1 });
    if (how !== 'click') {
      expect((await scrolled()).top, `${how} scrolled the drawer when it closed the label`).toBe(before.top);
    }
  });
}

/**
 * A cut label that has the focus keeps it when the drawer widens until the
 * label is no longer cut. At 1024 a Namespace's `NamespaceContentRemaining` is
 * cut; at 1440 it is whole. Both ways: focused and closed, then widened; and opened, widened, then closed with
 * Enter. Either way the focus stays on that label, and only on blur does it go
 * back to plain text with no tab stop.
 */
for (const opened of [false, true]) {
  test(`a focused cut label keeps the focus when the drawer widens and it is no longer cut (${opened ? 'open' : 'closed'})`, async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1024, height: 900 });
    await show(page, 'facts&kind=namespaces');
    await page.waitForSelector('[data-section="own"] tbody th');
    const label = page.locator('[data-fact-label]', { hasText: /^NamespaceContentRemaining$/ });
    await expect(label).toHaveAttribute('role', 'button');
    await label.focus();
    if (opened) {
      await page.keyboard.press('Enter');
      await expect(label).toHaveAttribute('aria-expanded', 'true');
    }

    const narrow = await page
      .locator('[data-facts-drawer]')
      .evaluate((drawer) => drawer.getBoundingClientRect().width);
    await page.setViewportSize({ width: 1440, height: 900 });
    // Wait for the drawer to be wider, and the label to fit in it, before
    // asking where the focus is. The drawer's width is what makes this a wait
    // when the label is open: an open label is never cut, so its fit alone is
    // already true at 1024.
    await page.waitForFunction((before) => {
      const drawer = document.querySelector('[data-facts-drawer]');
      const span = [...document.querySelectorAll('[data-fact-label]')].find(
        (one) => one.textContent === 'NamespaceContentRemaining',
      );
      return (
        drawer !== null &&
        drawer.getBoundingClientRect().width > before + 1 &&
        span !== undefined &&
        span.scrollWidth <= span.clientWidth + 1
      );
    }, narrow);
    await expect(label, 'the focus left the label when it stopped being cut').toBeFocused();

    // Open, Enter closes it; closed and no longer cut, there is nothing to
    // open, and Enter leaves it closed rather than widening its column.
    await page.keyboard.press('Enter');
    await expect(label).toHaveAttribute('aria-expanded', 'false');
    await expect(label, 'the focus left the label after Enter').toBeFocused();
    await page.keyboard.press('Enter');
    await expect(label, 'Enter opened a label that is no longer cut').toHaveAttribute('aria-expanded', 'false');

    await label.evaluate((span) => {
      (span as HTMLElement).blur();
    });
    await expect(label, 'an uncut label kept its button after blur').not.toHaveAttribute('role', 'button');
    await expect(label).not.toHaveAttribute('tabindex', /.*/);
  });
}

/**
 * At 390 the drawer's facts stack, and the four
 * condition types are still cut and are buttons; opened, one is laid out
 * whole and draws nothing past the drawer's edge.
 */
test('at 390 the cut condition labels are buttons, and an open one stays inside the drawer', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 900 });
  await show(page, 'facts&kind=namespaces');
  await page.waitForSelector('[data-section="own"] tbody th');
  const buttons = page.locator('[data-fact-label][role="button"]');
  await expect(buttons).toHaveCount(CONDITION_TYPES.length);
  expect((await buttons.allTextContents()).sort()).toEqual([...CONDITION_TYPES].sort());

  const label = buttons.filter({ hasText: 'NamespaceDeletionGroupVersionParsingFailure' });
  await label.focus();
  await page.keyboard.press('Enter');
  await expect(label).toHaveAttribute('aria-expanded', 'true');
  const after = await label.evaluate((span) => {
    const drawer = document.querySelector('[data-facts-drawer]');
    if (drawer === null) throw new Error('no drawer');
    return {
      cut: span.scrollWidth > span.clientWidth + 1,
      past:
        Math.max(...[...drawer.querySelectorAll('*')].map((one) => one.getBoundingClientRect().right)) -
        drawer.getBoundingClientRect().right,
      scroll: drawer.scrollWidth - drawer.clientWidth,
    };
  });
  expect(after.cut, 'the open label is still cut at 390').toBe(false);
  expect(after.past, 'a fact drawn past the drawer edge with the label open at 390').toBeLessThanOrEqual(0.5);
  expect(after.scroll).toBeLessThanOrEqual(0);
});
