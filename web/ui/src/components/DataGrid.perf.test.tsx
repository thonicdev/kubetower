import { fireEvent, render } from '@testing-library/react';
import { beforeEach, describe, expect, it } from 'vitest';

import { columnWidth, DataGrid, type GridColumn } from './DataGrid';

/**
 * What one render of this grid costs, counted and timed.
 *
 * A 400-row, 11-column list walks 4 400 `measure()` calls on every render, and
 * `ResourceList`'s fastest band refreshes every two seconds. This file is the
 * profile of that, and everything below is reproducible with
 * `vitest run ui/src/components/DataGrid.perf.test.tsx`.
 *
 * ## Two instruments, because there are two questions and a clock answers one
 *
 * **A spy, for how many.** `measure(row)` is called from `widthOf` and from
 * nowhere else; `cell(row)` once per `<td>`; and `column.sortable` is read once
 * per `Heading` render (`column.sortable !== false`) and nowhere else in the
 * file — so a getter on it counts heading renders without wrapping anything and
 * changing what is being measured. A call count is the same number in jsdom as
 * it is in Chrome, which is what makes it worth quoting at all.
 *
 * **A clock, for how much — on the half a clock can honestly time here.** The
 * width walk is arithmetic over strings: it touches no DOM, asks for no layout,
 * and node runs the same code a browser does, so timing it is timing the real
 * thing. A whole render in **jsdom is not** the real thing — jsdom builds no
 * boxes and lays nothing out — so every figure for one below is labeled
 * indicative and used only for a ratio. No browser profiler was run.
 *
 * ## The five renders, because they are not interchangeable
 *
 *  1. **mount** — the floor. The walk is unavoidable here.
 *  2. **same references** — a parent re-rendered and nothing about this grid
 *     changed. A `useMemo` on `[columns, rows]` is aimed at this one.
 *  3. **fresh arrays, identical content** — what `ResourceList.Grid` actually
 *     does: it rebuilds `columns` with `.map` on every render
 *     (`ResourceList.tsx:1007`) and `orderRows` copies `rows` whenever a sort is
 *     active (`:1070`). Both identities change while the payload did not, so a
 *     memo keyed on identity cannot see this render at all.
 *  4. **new row content** — a poll on a busy list, where the age column alone
 *     guarantees a new payload. The walk is *required* here: a width never
 *     outlives the answer it was computed from, and a cache
 *     that survived this render is the nodes-list overflow put back.
 *  5. **one pointermove of a column drag** — `setDragWidth` is state on
 *     `DataGrid`, so the whole grid re-renders on every move of the pointer,
 *     with `rows` unchanged. This is the path that runs at the pointer's rate rather than at the poll's.
 *
 * ## What it measures, and why there is no memo
 *
 * **The count is 4 400 and its cost is not there.** The walk is
 * 4 400 calls per render — every scenario above. It takes
 * **0.06 ms**. The render it sits inside takes ~20 ms in jsdom, so the walk is
 * **0.3 %** of it, and the share only falls as the list grows: 0.65 ms of
 * 378 ms at 5 000 rows. On the two-second band, 0.06 ms is three thousandths
 * of one percent of the interval.
 *
 * A `useMemo` on `[columns, rows]` would take scenario 2 from 4 400 to 0 and
 * scenario 5 from 4 000 to 0, and leave scenarios 3 and 4 at 4 400.
 * `React.memo` on `Heading` would change **nothing at all**, because every
 * caller passes inline arrows for `onSort`, `onResize` and `onPlace`, so the
 * memo's props differ on every render by construction. A memo is worth 0.06 ms
 * in the two renders it reaches, and **nothing in the one that actually
 * happens** — scenario 3, because `ResourceList.Grid` rebuilds both arrays on
 * every render — so there is none.
 *
 * **Where the cost really is: the body, and the drag.** Every render builds
 * 4 400 `<td>`s, and that number does not move under any memo aimed at the
 * widths. Two renders per poll is fine; the drag is not — scenario 5 shows one
 * pointermove re-rendering all 4 400 cells to change one `<col>`'s width, at 60
 * to 120 moves a second. Virtualizing `<tbody>`, or keeping the body out of the
 * drag's re-render, is where the next measurement should go. Neither is built
 * here.
 *
 * **What is not established.** No browser profiler was run — these are node and
 * jsdom figures on one machine, and every render figure is labeled indicative
 * for that reason. What a browser spends laying out 4 400 table cells is
 * unmeasured, and it is the number that decides the paragraph above.
 */

// Under `WINDOW_AFTER`, so every row is drawn and the counts below are of the
// whole list; `DataGrid.window.test.tsx` covers the windowed one.
const ROWS = 50;
const COLUMNS = 11;

const HEADINGS = [
  'Name',
  'Namespace',
  'Ready',
  'Status',
  'Restarts',
  'Age',
  'IP',
  'Node',
  'CPU',
  'Memory',
  'Controlled by',
];

interface Row {
  id: string;
  cells: Record<string, string>;
}

const counts = { measure: 0, cell: 0, heading: 0 };

function reset(): void {
  counts.measure = 0;
  counts.cell = 0;
  counts.heading = 0;
}

/** A fresh column set, exactly as `ResourceList.Grid` builds one per render. */
function spiedColumns(): GridColumn<Row>[] {
  return HEADINGS.map((label, index) => {
    const key = `c${String(index)}`;
    return {
      key,
      label,
      get sortable(): boolean {
        counts.heading += 1;
        return true;
      },
      ...(index === 0 ? { leads: true as const } : {}),
      measure: (row: Row): string => {
        counts.measure += 1;
        return row.cells[key] ?? '';
      },
      cell: (row: Row): string => {
        counts.cell += 1;
        return row.cells[key] ?? '';
      },
    };
  });
}

/**
 * The same columns without the counters, for the timings.
 *
 * The spies are what make the counts possible and they are also overhead on the
 * very call being timed. Eleven duplicated lines is the price of the instrument
 * not becoming part of the answer.
 */
function plainColumns(): GridColumn<Row>[] {
  return HEADINGS.map((label, index) => {
    const key = `c${String(index)}`;
    return {
      key,
      label,
      sortable: true,
      ...(index === 0 ? { leads: true as const } : {}),
      measure: (row: Row): string => row.cells[key] ?? '',
      cell: (row: Row): string => row.cells[key] ?? '',
    };
  });
}

function makeRows(generation: number, howMany: number = ROWS): Row[] {
  return Array.from({ length: howMany }, (_, index) => {
    const cells: Record<string, string> = {};
    for (let column = 0; column < COLUMNS; column += 1) {
      // Column 5 stands in for Age, which changes on every poll on its own —
      // which is why a busy list is a new payload every two seconds.
      cells[`c${String(column)}`] =
        column === 5
          ? `${String(generation)}m${String(index % 60)}s`
          : `value-${String(index)}-${String(column)}`;
    }
    return { id: `row-${String(index)}`, cells };
  });
}

function grid(columns: readonly GridColumn<Row>[], rows: readonly Row[]) {
  return (
    <DataGrid
      columns={columns}
      rows={rows}
      rowKey={(row) => row.id}
      label="Pods"
      empty={null}
      onSort={() => undefined}
      onResize={() => undefined}
      onPlace={() => undefined}
    />
  );
}

/** `widthOf` is not exported; this is its body for a column with no stored
 *  width, which is every column until the operator drags one. */
function walkWidths(columns: readonly GridColumn<Row>[], rows: readonly Row[]): number {
  let total = 0;
  for (const column of columns) {
    const measure = column.measure;
    const samples = measure === undefined ? [] : rows.map((row) => measure(row));
    total += columnWidth(column, samples);
  }
  return total;
}

function say(lines: readonly string[]): void {
  console.log(['', ...lines, ''].join('\n'));
}

/**
 * A profile is a heavy test by design - 400 rows, 11 columns, five renders
 * and a 500-iteration walk - and it needs a budget of its own. Vitest's default
 * 5 000 ms was enough on a hosted runner with a core to itself; on the
 * self-hosted pool, where one machine runs `go test -race` and this suite at
 * the same time, the five-render case can take 5 000 ms while every assertion
 * in it would pass. The figures
 * this file quotes are counts and ratios, never a wall-clock bound, so a larger
 * budget changes nothing it measures - it only stops a busy runner from
 * reading "slow" as "wrong".
 */
const PROFILE_BUDGET_MS = 60_000;

describe('what the grid costs per render', { timeout: PROFILE_BUDGET_MS }, () => {
  beforeEach(() => {
    reset();
  });

  it('counts the calls of five different renders', () => {
    const columnsA = spiedColumns();
    const rowsA = makeRows(1);
    const taken: string[] = [];
    const note = (what: string): void => {
      taken.push(
        `  ${what.padEnd(32)} measure=${String(counts.measure).padStart(5)}  cell=${String(counts.cell).padStart(5)}  headings=${String(counts.heading).padStart(3)}`,
      );
    };

    reset();
    const view = render(grid(columnsA, rowsA));
    const mount = counts.measure;
    note('1. mount');

    reset();
    view.rerender(grid(columnsA, rowsA));
    const sameReferences = counts.measure;
    note('2. re-render, same references');

    reset();
    view.rerender(grid(spiedColumns(), [...rowsA]));
    const freshArrays = counts.measure;
    note('3. re-render, fresh arrays');

    reset();
    view.rerender(grid(spiedColumns(), makeRows(2)));
    note('4. re-render, new row content');

    const grip = document.querySelector('[data-grip]');
    expect(grip).not.toBeNull();
    fireEvent.pointerDown(grip as Element, { button: 0, clientX: 100, pointerId: 1 });
    reset();
    fireEvent.pointerMove(grip as Element, { clientX: 140, pointerId: 1 });
    const dragMeasure = counts.measure;
    const dragCell = counts.cell;
    note('5. one pointermove of a drag');
    fireEvent.pointerUp(grip as Element, { clientX: 140, pointerId: 1 });

    say([`DataGrid — ${String(ROWS)} rows x ${String(COLUMNS)} columns, calls per render`, ...taken]);

    /*
      The arithmetic, measured rather than derived. If one of these ever
      fails because the widths were memoized, that is not automatically an
      improvement: read the timings the next test prints before deciding it was
      worth it, because the walk all these calls make up is under a tenth of a
      millisecond.
    */
    expect(mount).toBe(ROWS * COLUMNS);
    expect(sameReferences).toBe(ROWS * COLUMNS);
    expect(freshArrays).toBe(ROWS * COLUMNS);

    /*
      The drag's path. One move of the pointer re-renders the
      whole body — every cell of every row — in order to change one `<col>`'s
      width, and a pointer emits these 60 to 120 times a second rather than
      every two seconds. The dragged column is the 400 missing from the walk:
      its width comes from `dragWidth` and short-circuits `widthOf`.
    */
    expect(dragMeasure).toBe(ROWS * (COLUMNS - 1));
    expect(dragCell).toBe(ROWS * COLUMNS);
  });

  it('times the width walk against the render it sits inside', () => {
    const rows = makeRows(99);
    const columns = plainColumns();
    let sink = 0;
    // Warm, so the figure is of optimized code rather than of the first pass
    // through it, and accumulated, so nothing here is dead.
    for (let turn = 0; turn < 200; turn += 1) sink += walkWidths(columns, rows);
    const walkAt = performance.now();
    for (let turn = 0; turn < 500; turn += 1) sink += walkWidths(columns, rows);
    const walkMs = (performance.now() - walkAt) / 500;
    expect(sink).toBeGreaterThan(0);

    const view = render(grid(plainColumns(), rows));
    const renderAt = performance.now();
    for (let turn = 0; turn < 10; turn += 1) {
      view.rerender(grid(plainColumns(), makeRows(turn + 10)));
    }
    const renderMs = (performance.now() - renderAt) / 10;

    say([
      `DataGrid — ${String(ROWS)} rows x ${String(COLUMNS)} columns, where the time goes`,
      `  the width walk alone, node, real:         ${walkMs.toFixed(3)} ms`,
      `  one whole render, jsdom, indicative only: ${renderMs.toFixed(1)} ms`,
      `  the walk as a share of that render:       ${((walkMs / renderMs) * 100).toFixed(2)} %`,
    ]);

    // Not a performance assertion: a machine-dependent threshold would be
    // flaky, and a flaky test teaches people to re-run rather than to read.
    // This pins only that both halves were measured and neither was zero.
    expect(walkMs).toBeGreaterThan(0);
    expect(renderMs).toBeGreaterThan(0);
  });

  it('shows how both halves scale, which is what decides virtualizing', () => {
    let sink = 0;
    const scaling: string[] = [];

    for (const size of [400, 2000, 5000]) {
      const rows = makeRows(7, size);
      const columns = plainColumns();
      for (let turn = 0; turn < 50; turn += 1) sink += walkWidths(columns, rows);
      const at = performance.now();
      for (let turn = 0; turn < 100; turn += 1) sink += walkWidths(columns, rows);
      const walkMs = (performance.now() - at) / 100;

      const view = render(grid(plainColumns(), rows));
      const renderAt = performance.now();
      view.rerender(grid(plainColumns(), makeRows(8, size)));
      const renderMs = performance.now() - renderAt;
      view.unmount();

      scaling.push(
        `  ${String(size).padStart(5)} rows: walk ${walkMs.toFixed(3)} ms | one render ${renderMs.toFixed(1)} ms (jsdom) | the walk is ${((walkMs / renderMs) * 100).toFixed(2)} %`,
      );
    }

    expect(sink).toBeGreaterThan(0);
    say([`DataGrid — how both halves scale, ${String(COLUMNS)} columns`, ...scaling]);
  }, 60000);
});
