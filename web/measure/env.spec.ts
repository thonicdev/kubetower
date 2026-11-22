import { expect, test } from '@playwright/test';
import { px, show } from './layoutkit';

/* -------------------------------------------------------------------------- */
/* 10 — the environment table keeps its value column under long names        */
/* -------------------------------------------------------------------------- */

/**
 * The drawer's environment table, at the drawer's width, with seven names of
 * 47 to 51 characters: six optional references to keys the ConfigMap does not
 * carry, then one required key that is missing.
 *
 * jsdom lays nothing out, so `Containers.test.tsx` stays green over a value
 * column a dozen characters wide. `break-all` on a cell `.kt-grid` holds at
 * `white-space: nowrap` is inert: every name stays one line and its column
 * grows to fit the longest.
 *
 * The numbers are relations to the row's own line height, never a font's
 * advance (limit 4 in `main.tsx`), except the two declared widths: the name
 * column's `w-48` bound, and the value column's floor.
 *
 * **Whole words, then lines.** Names and keys are offered a break after each
 * `_`, `.` and `-` (`atTheSeparators`). Broken anywhere instead, a 51-character
 * name can leave one letter on a third line, and a key `t` or `mit`. So the first assertion is that no segment between two
 * separators is split across lines. The price is a line: a 47-character name
 * that filled two lines when it could be cut mid-word takes three in whole
 * words (`ARGOCD_APPLICATION_` / `CONTROLLER_STATUS_` / `PROCESSORS`), and the
 * bound on a name is three lines, not two.
 */
test('the environment table keeps a readable value column, and breaks names and keys only between words', async ({
  page,
}) => {
  await page.setViewportSize({ width: 1600, height: 900 });
  await show(page, 'env');
  await page.waitForSelector('[data-env] tr');

  const measured = await page.evaluate(() => {
    const rows = [...document.querySelectorAll('[data-env] tbody tr')];
    return rows.map((tr) => {
      const th = tr.querySelector('th');
      const td = tr.querySelector('td');
      if (th === null || td === null) throw new Error('a row without its two cells');
      const value = td.querySelector('[data-absence]');
      // Lines are counted, not divided out of a height: a table cell is as tall
      // as its row, so the name cell's box says nothing about the name. Every
      // fragment of text has a box; fragments whose tops are within half a line
      // of each other are on one line - a sans word and a monospace one beside
      // it do not share a top to the pixel.
      const half = Number.parseFloat(getComputedStyle(td).lineHeight) / 2;
      const lines = (element: Element): number => {
        const tops: number[] = [];
        const walker = document.createTreeWalker(element, NodeFilter.SHOW_TEXT);
        for (let node = walker.nextNode(); node !== null; node = walker.nextNode()) {
          const range = document.createRange();
          range.selectNodeContents(node);
          for (const rect of range.getClientRects()) {
            if (rect.width > 0) tops.push(rect.top);
          }
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
      // How many text nodes are laid out on more than one line. Each segment
      // `atTheSeparators` emits is a text node of its own, so one on two lines
      // is a word that was cut.
      const split = (element: Element | null): number => {
        if (element === null) return 0;
        let count = 0;
        const walker = document.createTreeWalker(element, NodeFilter.SHOW_TEXT);
        for (let node = walker.nextNode(); node !== null; node = walker.nextNode()) {
          const range = document.createRange();
          range.selectNodeContents(node);
          const tops = [...range.getClientRects()].filter((rect) => rect.width > 0).map((rect) => rect.top);
          if (tops.length > 0 && Math.max(...tops) - Math.min(...tops) > half) count += 1;
        }
        return count;
      };
      return {
        name: (th.textContent ?? '').trim(),
        nameSplit: split(th),
        keySplit: split(td.querySelector('[data-source-key]')),
        nameWidth: th.getBoundingClientRect().width,
        nameLines: lines(th),
        valueWidth: td.getBoundingClientRect().width,
        valueLines: lines(td),
        absence: value?.getAttribute('data-absence') ?? '',
        absenceLines: value === null ? 0 : lines(value),
      };
    });
  });

  expect(measured.length, 'the scene draws seven rows').toBe(7);

  for (const row of measured) {
    const at = `${row.name} (${row.absence})`;
    // The name is bounded: `w-48` is what it may take, and it breaks inside that.
    expect(row.nameWidth, `${at}: the name column is ${px(row.nameWidth)}, past w-48`).toBeLessThanOrEqual(
      192.5,
    );
    // The floor. It was a dozen characters.
    expect(row.valueWidth, `${at}: the value column is ${px(row.valueWidth)}`).toBeGreaterThanOrEqual(320);
    expect(row.nameSplit, `${at}: a part of the name is cut across two lines`).toBe(0);
    expect(row.keySplit, `${at}: a part of the key is cut across two lines`).toBe(0);
    expect(row.nameLines, `${at}: the name is ${String(row.nameLines)} lines`).toBeLessThanOrEqual(3);
  }

  // An absent optional key is one short value on one line. Its address names
  // the object on every row, so at 13px it takes two lines of its own in the
  // value column's floor, cut only between words: the cell is no more than
  // three lines.
  const optional = measured.filter((each) => each.absence === 'expected');
  expect(optional.length, 'six optional keys the ConfigMap does not carry').toBe(6);
  for (const row of optional) {
    expect(row.absenceLines, `${row.name}: the dash for an absent key is ${String(row.absenceLines)} lines`).toBe(1);
    expect(row.valueLines, `${row.name}: the value and its address take ${String(row.valueLines)} lines`).toBeLessThanOrEqual(3);
  }

  // Not bounded, and on purpose: a required key that is missing keeps the
  // server's sentence, because it is why the container will not start. Only
  // that it is still the sentence is checked here; `Containers.test.tsx` holds
  // its words and its tone.
  const required = measured.find((each) => each.absence === 'unset');
  expect(required?.absenceLines ?? 0, 'the missing required key still draws its sentence').toBeGreaterThanOrEqual(1);
});
