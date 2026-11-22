import { expect, test, type Page } from '@playwright/test';
import { VIEWPORTS, answerTheCluster, px, show } from './layoutkit';

/* -------------------------------------------------------------------------- */
/* 9 — a focused control's ring is not cut off by the box that scrolls it      */
/* -------------------------------------------------------------------------- */

/**
 * The ring is an outline, and an outline is drawn **outside** the control's
 * border. Any ancestor whose `overflow` is not `visible` clips at its padding
 * edge, so a control laid flush against a scroll region loses whatever part of
 * its ring falls past that edge. jsdom draws no outline and clips nothing, so no
 * unit test can see it.
 *
 * Where a ring can be cut, and by what, at every viewport the dialog cases use:
 *
 * | | Control | Box that can cut it |
 * |---|---|---|
 * | pod-delete confirmation | the typed field, focused on open | the dialog body |
 * | | the commit button | the dialog body |
 * | | Cancel | the dialog body |
 * | 203-target plan | the target list's own focus outline | the dialog body |
 * | kinds menu | each group heading, drawing the browser's ring | the menu's scroll region |
 * | dashboard | a link inside a pill | the pill's `truncate` |
 * | compact list | an address's copy button | its cell |
 * | | the namespace filter | its cell |
 *
 * **What it reads.** The focused element's border box, grown by its outline's
 * width plus its offset (a negative offset shrinks it), against the padding box
 * of every ancestor that clips - on an axis where that ancestor scrolls, against
 * the whole of what it scrolls over, so the reading does not depend on where
 * Tab happened to leave the scroll. One number per side, and a failure names
 * the control, the side, the pixels and the box that took them.
 *
 * Comparing against only the part on screen would depend on where Tab left
 * the scroll, and so on the runner's fonts; and asking the shell's frame
 * outside a scrolling page would ask the window. So a box outside one that
 * scrolls on an axis is not asked about that axis.
 *
 * **What it does not read.** Only what Tab reaches, and only in the scenes
 * named below. Nothing here judges a control that is itself wider than the box
 * showing it: at 420px the dashboard's tables scroll sideways and a long name
 * is cut off by design, ring and all, which is why the dashboard is walked at
 * 1280px only. And a control that sits at a scrolled edge is not judged - one
 * more line of scroll shows its ring whole.
 */
interface RingReading {
  name: string;
  lit: boolean;
  cut: { left: number; top: number; right: number; bottom: number };
  by: string;
}

async function ringOfFocused(page: Page): Promise<RingReading | null> {
  return page.evaluate(() => {
    const element = document.activeElement;
    if (!(element instanceof HTMLElement) || element === document.body) return null;

    const style = getComputedStyle(element);
    const width = style.outlineStyle === 'none' ? 0 : Number.parseFloat(style.outlineWidth);
    const reach = width === 0 ? 0 : width + Number.parseFloat(style.outlineOffset);
    const box = element.getBoundingClientRect();
    const ring = {
      left: box.left - reach,
      top: box.top - reach,
      right: box.right + reach,
      bottom: box.bottom + reach,
    };

    const sides = ['left', 'top', 'right', 'bottom'] as const;
    const cut = { left: 0, top: 0, right: 0, bottom: 0 };
    let by = '(nothing)';
    // Once a box that scrolls on an axis has been passed, the boxes outside it
    // no longer decide that axis: what they show of the control depends on
    // where the inner one is scrolled to, and the inner one can always be
    // scrolled to show it. Without this, the shell's own `overflow-hidden`
    // frame - the window, in effect - read a link Tab had scrolled to the foot
    // of the page as cut.
    let pastX = false;
    let pastY = false;
    for (let above = element.parentElement; above !== null; above = above.parentElement) {
      const clips = getComputedStyle(above);
      if (clips.overflowX === 'visible' && clips.overflowY === 'visible') continue;
      // Per axis. Where the box scrolls on an axis, the edge that counts is its
      // scrolled content's, not the part on screen: a control Tab has just
      // scrolled to the bottom of the page is cut by where the scroll stopped,
      // and one more line of scroll shows it whole. A ring past the end of the
      // content is cut at every scroll position, because an outline adds
      // nothing to what a box can scroll to. Where the box only clips -
      // `hidden`, `clip`, a `truncate` - what is on screen is all there is.
      const scrolls = (overflow: string) => overflow === 'auto' || overflow === 'scroll';
      const outer = above.getBoundingClientRect();
      const left = outer.left + above.clientLeft - (scrolls(clips.overflowX) ? above.scrollLeft : 0);
      const top = outer.top + above.clientTop - (scrolls(clips.overflowY) ? above.scrollTop : 0);
      const wide = scrolls(clips.overflowX) ? above.scrollWidth : above.clientWidth;
      const tall = scrolls(clips.overflowY) ? above.scrollHeight : above.clientHeight;
      const onX = clips.overflowX !== 'visible' && !pastX;
      const onY = clips.overflowY !== 'visible' && !pastY;
      const here = {
        left: onX ? Math.max(0, left - ring.left) : 0,
        top: onY ? Math.max(0, top - ring.top) : 0,
        right: onX ? Math.max(0, ring.right - (left + wide)) : 0,
        bottom: onY ? Math.max(0, ring.bottom - (top + tall)) : 0,
      };
      if (scrolls(clips.overflowX)) pastX = true;
      if (scrolls(clips.overflowY)) pastY = true;
      if (sides.some((side) => here[side] > cut[side])) {
        by = `<${above.tagName.toLowerCase()} class="${above.className}">`;
        for (const side of sides) cut[side] = Math.max(cut[side], here[side]);
      }
    }

    const label = element.getAttribute('aria-label') ?? (element.textContent ?? '').trim().slice(0, 40);
    return {
      name: `${element.tagName.toLowerCase()} "${label === '' ? '(unnamed)' : label}"`,
      lit: element.matches(':focus-visible') && width > 0,
      cut,
      by,
    };
  });
}

function expectRingWhole(reading: RingReading, where: string): void {
  for (const side of ['left', 'top', 'right', 'bottom'] as const) {
    expect(
      reading.cut[side],
      `${where}: ${reading.name}'s focus ring is cut ${px(reading.cut[side])} on the ${side} by ${reading.by}`,
    ).toBeLessThanOrEqual(0.5);
  }
}

/**
 * Tab through a scene and read every control the keyboard lands on. Stops when
 * focus leaves `within`, when it comes back round to the first control read,
 * or after `limit` presses.
 */
async function walkRings(page: Page, within: string | null, limit = 120): Promise<RingReading[]> {
  const seen: RingReading[] = [];
  for (let step = 0; step < limit; step += 1) {
    await page.keyboard.press('Tab');
    // By identity and not by name: a dashboard has several links called
    // "Pods", and the first of them coming round again is not the end.
    const where = await page.evaluate((selector) => {
      const element = document.activeElement;
      const first: unknown = Reflect.get(window, 'ktRingFirst');
      if (first === undefined && element !== null && element !== document.body) {
        Reflect.set(window, 'ktRingFirst', element);
      }
      return {
        again: first !== undefined && element === first,
        inside: selector === null || (element?.closest(selector) ?? null) !== null,
      };
    }, within);
    if (where.again || !where.inside) break;
    const reading = await ringOfFocused(page);
    if (reading !== null) seen.push(reading);
  }
  return seen;
}

test('the confirmation dialog draws its focused field and its buttons with their whole ring', async ({
  page,
}) => {
  await answerTheCluster(page);

  for (const viewport of VIEWPORTS) {
    await page.setViewportSize(viewport);
    await page.goto('/?scene=dialog-typed');
    await page.waitForSelector('[role="dialog"] input');
    const where = `delete a pod, ${String(viewport.width)}x${String(viewport.height)}`;

    // What the screenshot showed: the dialog focuses the field on open, so its
    // ring is lit before any key is pressed.
    const opened = await ringOfFocused(page);
    if (opened === null) throw new Error(`${where}: nothing has focus once the dialog is open`);
    expect(opened.name, `${where}: the dialog did not focus its typed field`).toMatch(/^input /);
    expect(opened.lit, `${where}: the typed field is focused and draws no ring`).toBe(true);
    expectRingWhole(opened, where);

    // Satisfied, so the commit button is enabled and Tab reaches it.
    await page.keyboard.type('1');
    const walked = await walkRings(page, '[role="dialog"]');
    const names = walked.map((one) => one.name);
    expect(names, `${where}: Tab did not reach the commit button`).toContain('button "Delete this pod"');
    expect(names, `${where}: Tab did not reach Cancel`).toContain('button "Cancel"');
    for (const reading of walked) expectRingWhole(reading, where);
  }
});

for (const scene of ['dialog', 'menu', 'list-controls', 'list-controls&compact=1']) {
  test(`every control in ${scene} draws its whole focus ring`, async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 720 });
    await show(page, scene);
    // Each waits for the thing it is about, rather than measuring a scene
    // that has not drawn it yet.
    if (scene === 'dialog') await page.waitForSelector('[role="dialog"] ul');
    if (scene === 'menu') await page.waitForSelector('#menu summary');
    if (scene.startsWith('list-controls')) await page.waitForSelector('[data-role="namespace"]');

    const walked = await walkRings(page, null);
    expect(walked.length, `${scene}: Tab reached no control at all`).toBeGreaterThan(0);
    for (const reading of walked) expectRingWhole(reading, scene);
  });
}

test('every control on the dashboard draws its whole focus ring at 1280px', async ({ page }) => {
  // The one scene answered by its own fixture rather than from here - see
  // `main.tsx` - and walked for the reason the others are: it is the whole
  // console, rail, menu, top bar and page, in one tab order.
  await page.setViewportSize({ width: 1280, height: 720 });
  await page.goto('/?scene=overview');
  await page.waitForSelector('#page h1');

  const walked = await walkRings(page, null);
  expect(walked.length, 'the dashboard: Tab reached fewer controls than it draws').toBeGreaterThan(20);
  for (const reading of walked) expectRingWhole(reading, 'the dashboard');
});
