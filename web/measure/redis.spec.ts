import { expect, test } from '@playwright/test';
import { px, show } from './layoutkit';

/* -------------------------------------------------------------------------- */
/* The Redis viewer's hash table: a long field name is one line, ≤ 40 %       */
/* -------------------------------------------------------------------------- */

// The scene's first field, spelt as `main.tsx` spells it: the spec runs in
// Node and cannot import the harness, which draws.
const LONG_FIELD = `${'session-attribute-with-a-very-long-name-'.repeat(8)}end`;

/** The cap the panel sets, and what the cell's padding may add to it. */
const SHARE = 0.4;
const TOLERANCE = 16;

test('a hash field far longer than the table is one line within about 40 % of it, whole in its title, at 1440px', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await show(page, 'redis-hash');
  await page.getByRole('button', { name: /session:1/ }).click();
  await page.waitForSelector('[data-entries="hash"] [data-field]');

  const reading = await page.evaluate(() => {
    const table = document.querySelector('[data-entries="hash"]');
    const field = document.querySelector('[data-entries="hash"] [data-field]');
    if (!(table instanceof HTMLElement) || !(field instanceof HTMLElement)) throw new Error('no hash table');
    const cell = field.closest('td');
    const value = cell?.nextElementSibling;
    if (!(cell instanceof HTMLElement) || !(value instanceof HTMLElement)) throw new Error('no cells');
    const style = getComputedStyle(field);
    return {
      table: table.getBoundingClientRect().width,
      cell: cell.getBoundingClientRect().width,
      value: value.getBoundingClientRect().width,
      fieldHeight: field.getBoundingClientRect().height,
      lineHeight: Number.parseFloat(style.lineHeight),
      cut: field.scrollWidth > field.clientWidth,
      ellipsis: style.textOverflow,
      title: field.getAttribute('title'),
      documentScrollWidth: document.documentElement.scrollWidth,
      documentClientWidth: document.documentElement.clientWidth,
    };
  });

  expect(reading.fieldHeight, `the field is ${px(reading.fieldHeight)} tall where one line is ${px(reading.lineHeight)}`).toBeLessThanOrEqual(
    reading.lineHeight + 1,
  );
  expect(
    reading.cell,
    `the field cell is ${px(reading.cell)} of a ${px(reading.table)} table: more than ${String(SHARE * 100)} % and ${String(TOLERANCE)}px`,
  ).toBeLessThanOrEqual(reading.table * SHARE + TOLERANCE);
  // Capped, not collapsed: a name this long takes the whole cap.
  expect(reading.cell, `the field cell is ${px(reading.cell)}, far under its cap`).toBeGreaterThanOrEqual(reading.table * SHARE - TOLERANCE);
  expect(reading.value, `the value keeps ${px(reading.value)}, under half the table`).toBeGreaterThanOrEqual(reading.table * 0.45);
  expect(reading.cut, 'the name is cut, not drawn whole past its cell').toBe(true);
  expect(reading.ellipsis).toBe('ellipsis');
  expect(reading.title).toBe(LONG_FIELD);
  expect(reading.documentScrollWidth - reading.documentClientWidth, 'the document scrolls sideways').toBeLessThanOrEqual(0);
});
