import { expect, test, type Page } from '@playwright/test';
import { px, show } from './layoutkit';

interface Arm {
  armLeft: number;
  first: { name: string; left: number; top: number };
  watching: readonly { name: string; left: number; right: number; top: number; height: number }[];
  verbs: readonly { name: string; left: number; right: number; top: number; height: number }[];
  spacer: { left: number; right: number } | null;
}

async function readArm(page: Page): Promise<Arm> {
  return page.evaluate(() => {
    const arm = document.querySelector('[data-object-controls]');
    if (!(arm instanceof HTMLElement)) throw new Error('no [data-object-controls] arm');

    const verbsGroup = arm.querySelector('[role="group"][aria-label="Actions"]');
    const spacer = arm.querySelector(':scope > [data-arm-spacer]');

    function describe(element: Element) {
      const box = element.getBoundingClientRect();
      return {
        name: (element.getAttribute('aria-label') ?? element.textContent ?? '').trim(),
        left: box.left,
        right: box.right,
        top: box.top,
        height: box.height,
      };
    }

    const buttons = [...arm.querySelectorAll('button')];
    const verbs = buttons.filter((button) => verbsGroup?.contains(button) === true).map(describe);
    const watching = buttons
      .filter((button) => verbsGroup?.contains(button) !== true)
      .map(describe);

    const style = getComputedStyle(arm);
    const first = watching[0];
    if (first === undefined) throw new Error('the arm carries no watching control');

    return {
      armLeft: arm.getBoundingClientRect().left + Number.parseFloat(style.paddingLeft),
      first: { name: first.name, left: first.left, top: first.top },
      watching,
      verbs,
      spacer:
        spacer === null
          ? null
          : {
              left: spacer.getBoundingClientRect().left,
              right: spacer.getBoundingClientRect().right,
            },
    };
  });
}

test('the drawer puts the watching controls before the spacer and the verbs after it, in the same place on every kind', async ({
  page,
}) => {
  await page.setViewportSize({ width: 1600, height: 900 });

  await show(page, 'arm-pod');
  await page.waitForSelector('[data-object-controls] button');
  const pod = await readArm(page);

  expect(
    pod.watching.map((control) => control.name),
    'a Pod offers Logs, Shell and Claude, in that order, before the spacer',
  ).toEqual(['Logs', 'Shell', 'Claude']);
  expect(pod.verbs.length, 'a Pod offers verbs').toBeGreaterThan(0);
  expect(pod.spacer, 'a Pod with verbs has the spacer between the two kinds of control').not.toBeNull();

  // The three watching controls are one set, so each pair stands the same
  // distance apart. Logs is a child of the row and Shell and Claude are
  // children of `Terminals`' group, so two gaps decide this one spacing, and
  // they can drift apart. Measured here because jsdom
  // lays nothing out: `ObjectDetail.test.tsx` pins the classes, this the pixels.
  const gaps: number[] = [];
  for (let index = 1; index < pod.watching.length; index += 1) {
    const before = pod.watching[index - 1];
    const after = pod.watching[index];
    if (before === undefined || after === undefined) continue;
    gaps.push(after.left - before.right);
  }
  expect(
    Math.max(...gaps) - Math.min(...gaps),
    `the watching controls stand ${gaps.map(px).join(' and ')} apart, where one set has one gap`,
  ).toBeLessThanOrEqual(0.5);

  // One arm: every control on one line. A second arm would show as a control
  // on a different row.
  for (const control of [...pod.watching, ...pod.verbs]) {
    expect(
      Math.abs(control.top - pod.first.top),
      `${control.name} is on a different row from ${pod.first.name}: ${px(control.top)} against ${px(pod.first.top)}`,
    ).toBeLessThanOrEqual(2);
  }

  const spacer = pod.spacer;
  if (spacer === null) throw new Error('unreachable: asserted not null above');
  for (const control of pod.watching) {
    expect(
      spacer.left - control.right,
      `${control.name} is a watching control and must sit before the spacer: it ends at ${px(control.right)}, the spacer starts at ${px(spacer.left)}`,
    ).toBeGreaterThanOrEqual(-0.5);
  }
  for (const verb of pod.verbs) {
    expect(
      verb.left - spacer.right,
      `${verb.name} is a verb and must sit after the spacer: it starts at ${px(verb.left)}, the spacer ends at ${px(spacer.right)}`,
    ).toBeGreaterThanOrEqual(-0.5);
  }

  // And the verbs keep their distance: 12px on each side of the spacer, the
  // spacer's own margin plus the set's gap; `ObjectDetail.test.tsx` pins the
  // same sum in classes.
  const lastWatching = pod.watching[pod.watching.length - 1];
  const firstVerb = pod.verbs[0];
  if (lastWatching === undefined || firstVerb === undefined) {
    throw new Error('unreachable: both lists asserted non-empty above');
  }
  for (const [what, distance] of [
    [`${lastWatching.name} to the spacer`, spacer.left - lastWatching.right],
    [`the spacer to ${firstVerb.name}`, firstVerb.left - spacer.right],
  ] as const) {
    expect(Math.abs(distance - 12), `${what} is ${px(distance)}, where it was 12px`).toBeLessThanOrEqual(0.5);
  }

  // The arm's leading edge is unconditional, so the first watching control sits
  // at the same x whatever the object is: Logs on a Pod and Terminals on a
  // Namespace start at the same place.
  await show(page, 'arm-namespace');
  await page.waitForSelector('[data-object-controls] button');
  const namespace = await readArm(page);

  expect(namespace.verbs.length, 'a Namespace here offers no verbs').toBe(0);
  expect(
    Math.abs(namespace.first.left - pod.first.left),
    `the first control is at ${px(pod.first.left)} on a Pod (${pod.first.name}) and ${px(namespace.first.left)} on a Namespace (${namespace.first.name})`,
  ).toBeLessThanOrEqual(1);
  expect(
    Math.abs(pod.first.left - pod.armLeft),
    `nothing conditional may precede the first control: it starts at ${px(pod.first.left)} and the arm's content at ${px(pod.armLeft)}`,
  ).toBeLessThanOrEqual(1);
});

/**
 * Every control on the arm is as tall as a verb, whichever controls are
 * present.
 *
 * Claude's button is a mark with no word, so its own content is 15px where a
 * neighbor's is one 18px line of `text-xs`. Stretching it to the group's
 * height works only where a taller sibling is in the group: on a Namespace,
 * Claude is the one terminal control and would stand shorter than the verbs.
 * jsdom lays nothing out, so the pixels are read here.
 */
test('every control on the arm is as tall as a verb, on a Pod and on a Namespace', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 900 });

  await show(page, 'arm-pod');
  await page.waitForSelector('[data-object-controls] button[aria-label="Claude"]');
  const pod = await readArm(page);
  const verb = pod.verbs[0];
  if (verb === undefined) throw new Error('a Pod offers verbs, and this scene drew none');

  await show(page, 'arm-namespace');
  await page.waitForSelector('[data-object-controls] button[aria-label="Claude"]');
  const namespace = await readArm(page);

  for (const [where, arm] of [
    ['Pod', pod],
    ['Namespace', namespace],
  ] as const) {
    for (const control of [...arm.watching, ...arm.verbs]) {
      expect(
        Math.abs(control.height - verb.height),
        `${control.name} on a ${where} is ${px(control.height)} tall, where the verb ${verb.name} is ${px(verb.height)}`,
      ).toBeLessThanOrEqual(0.5);
    }
  }
});
