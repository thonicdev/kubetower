import { fireEvent, render, screen, within } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import { absorb, columnWidth, DataGrid, headingRoom, isSized, postable, type GridColumn } from './DataGrid';

interface Thing {
  id: string;
  name: string;
  cpu: string;
}

const rows: Thing[] = [
  { id: 'a', name: 'a-very-long-pod-name-that-would-widen-anything', cpu: '1163m' },
  { id: 'b', name: 'b', cpu: '368m' },
];

function columns(overrides: Partial<GridColumn<Thing>>[] = []): GridColumn<Thing>[] {
  const base: GridColumn<Thing>[] = [
    { key: 'name', label: 'Name', measure: (row) => row.name, cell: (row) => row.name },
    { key: 'cpu', label: 'CPU', align: 'end', measure: (row) => row.cpu, cell: (row) => row.cpu },
  ];
  return base.map((column, index) => ({ ...column, ...overrides[index] }));
}

function widths(): string[] {
  return [...document.querySelectorAll('col')].map((col) => (col as HTMLElement).style.width);
}

/**
 * Each column's fraction of the colgroup, whatever unit the colgroup is written
 * in.
 *
 * A sized table declares percentages and an unsized one pixels — `declareWidth`
 * says why — so a test that compares the two has to compare them in the one unit
 * both can be read in. It is also the unit the claim is actually about: what a
 * width decides is this column's portion of the table, and jsdom lays nothing
 * out, so the portion is the whole of what is assertable here.
 */
function shares(): number[] {
  const declared = widths().map((width) => Number.parseFloat(width));
  const total = declared.reduce((sum, each) => sum + each, 0);
  return declared.map((each) => (total === 0 ? 0 : each / total));
}

describe('the list at its edges', () => {
  // jsdom lays nothing out, so these pin the classes the geometry depends on;
  // `measure/table.spec.ts` reads the pixels.
  function headBoxes(): HTMLElement[] {
    return [...document.querySelectorAll('thead th')].map(
      (th) => th.firstElementChild as HTMLElement,
    );
  }

  it('aligns an end-aligned heading with its values, not only its text', () => {
    // `text-right` on the cell moved nothing: the label is in a flex box, and a
    // flex box's children follow `justify-content`.
    render(<DataGrid columns={columns()} rows={rows} rowKey={(r) => r.id} label="Pods" empty={null} />);
    const [name, cpu] = headBoxes();
    expect(cpu?.className).toContain('justify-end');
    expect(name?.className).not.toContain('justify-end');
  });

  it('keeps the headings at the top of the parent that scrolls, where asked', () => {
    render(
      <DataGrid columns={columns()} rows={rows} rowKey={(r) => r.id} label="Pods" empty={null} stickyHead />,
    );
    for (const th of document.querySelectorAll('thead th')) {
      expect(th.className).toContain('sticky');
      // `#panel` has no padding, so the heading sits at its very top (see
      // `STICKY`).
      expect(th.className).toContain('top-0');
      expect(th.className).not.toContain('-top-3');
    }
    // The box that held the sticky inert: overflowing one axis made it the
    // scrollport in both, the height of the whole table.
    expect(document.querySelector('.table-scroll')?.className).not.toContain('overflow-x-auto');
  });

  it('leaves a table with no such parent its own sideways scroll, and no sticky', () => {
    render(<DataGrid columns={columns()} rows={rows} rowKey={(r) => r.id} label="Pods" empty={null} />);
    expect(document.querySelector('.table-scroll')?.className).toContain('overflow-x-auto');
    expect(document.querySelector('thead th')?.className).not.toContain('sticky');
  });
});

describe('the grid', () => {
  it('is kt-grid and never grid', () => {
    // Tailwind emits `.grid { display: grid }`. A table wearing that class is
    // out of table layout, and both of `theme.css`'s rules - the inherited
    // white-space and the fixed layout - then have
    // nothing to act on. It still looks like a table, which is why this is a
    // test rather than a comment.
    render(<DataGrid columns={columns()} rows={rows} rowKey={(r) => r.id} label="Pods" empty={null} />);
    const table = screen.getByRole('table');
    expect(table.classList.contains('kt-grid')).toBe(true);
    expect(table.classList.contains('grid')).toBe(false);
  });

  describe('the widths: never older than the rows they size', () => {
    // Four assertions. The fourth is the one only React can make.

    it('gives a column with nothing under it its heading’s full room', () => {
      // `TestAListWithNoRowsStillDrawsItsHeadingsWhole`: otherwise the heading
      // draws ellipsized on a list where it is the only thing on screen.
      const column = { key: 'selector', label: 'Node selector', cell: () => null };
      expect(columnWidth(column, [])).toBeGreaterThanOrEqual(headingRoom('Node selector'));
    });

    it('gives a column blank on every row its heading’s full room too', () => {
      // `TestAColumnThatIsBlankOnEveryRowIsStillSizedByItsHeading`: the same
      // defect one column wide, and it is not the rare case.
      const column = { key: 'overhead', label: 'Overhead', cell: () => null };
      expect(columnWidth(column, ['', '', ''])).toBeGreaterThanOrEqual(headingRoom('Overhead'));
    });

    it('never makes a column narrower than its heading', () => {
      // A heading that gave way to its values read `RE…` over Ready and
      // Restarts both at 900px: the column was what its digits needed, and the
      // heading was cut to two letters. A list that cannot fit scrolls
      // sideways instead (`theme.css`, `min-width` on an unsized table).
      for (const [label, values] of [
        ['Ready', ['1/1']],
        ['Restarts', ['883']],
        ['Containers', ['2']],
        ['Age', ['9d']],
      ] as const) {
        const width = columnWidth({ key: label, label, cell: () => null }, values);
        expect(width, label).toBeGreaterThanOrEqual(headingRoom(label));
      }
      // And the values still win where they are the wider of the two.
      const status = columnWidth({ key: 's', label: 'Status', cell: () => null }, ['CrashLoopBackOff']);
      expect(status).toBeGreaterThan(headingRoom('Status'));
    });

    it('gives an uppercase heading the room it is drawn in', () => {
      // A heading is drawn uppercase and tracked, wider per character than a
      // value: measured at 72px for `CONTAINERS`, 54 for `RESTARTS`, plus 37
      // for the paddings, the sort mark and the resize grip beside it.
      expect(headingRoom('Containers')).toBeGreaterThanOrEqual(72 + 37);
      expect(headingRoom('Restarts')).toBeGreaterThanOrEqual(54 + 37);
    });

    it('revises the colgroup when a later answer arrives', () => {
      // What React buys here: the colgroup is rendered from the same rows as
      // the tbody, so a colgroup describing an older answer than the rows it
      // sizes cannot exist. Otherwise usage columns sized for the em dash that
      // stood in for a reading would overflow when the reading arrives.
      const late: Thing[] = [{ id: 'a', name: 'a', cpu: '—' }];
      const view = render(
        <DataGrid columns={columns()} rows={late} rowKey={(r) => r.id} label="Nodes" empty={null} />,
      );
      const withDash = widths();

      view.rerender(
        <DataGrid
          columns={columns()}
          rows={[{ id: 'a', name: 'a', cpu: '1163m / 4000m' }]}
          rowKey={(r) => r.id}
          label="Nodes"
          empty={null}
        />,
      );
      expect(widths()[1]).not.toBe(withDash[1]);
    });

    it('reserves what a column says a late value costs, on the render that has none', () => {
      // The declarative half, and the remedy for the stillness the paragraph
      // above gives up. No resource column declares one today because `Column`
      // carries no statement of what a column draws - a contract gap, recorded.
      const plain = columnWidth({ key: 'cpu', label: 'CPU', cell: () => null }, ['—']);
      const reserving = columnWidth(
        { key: 'cpu', label: 'CPU', reserve: 220, cell: () => null },
        ['—'],
      );
      expect(reserving).toBe(220);
      expect(reserving).toBeGreaterThan(plain);
    });
  });

  it('honors a stored width over its own arithmetic', () => {
    /*
      Asserted as a **share** and not as `77px`, because a sized table writes
      each column's portion of the set into the colgroup rather than its pixels
      — `declareWidth` says why. The claim: 77 is used where the arithmetic
      would have produced something
      else, and it is used against the arithmetic's own number for the column
      beside it.
    */
    const cpu = columnWidth(columns()[1] as GridColumn<Thing>, rows.map((row) => row.cpu));
    render(
      <DataGrid
        columns={columns([{ width: 77 }])}
        rows={rows}
        rowKey={(r) => r.id}
        label="Pods"
        empty={null}
      />,
    );
    expect(widths()[0]).toBe(`${String(Math.round((77 / (77 + cpu)) * 1e6) / 1e4)}%`);
    expect(shares()[0]).toBeCloseTo(77 / (77 + cpu), 5);
  });

  it('declares pixels until one is stored, and shares from then on', () => {
    // The unit is the statement: an unsized table's numbers are minimums — what
    // it takes to draw each column — and a sized one's are portions of a table
    // that is always exactly its panel.
    render(<DataGrid columns={columns()} rows={rows} rowKey={(r) => r.id} label="Pods" empty={null} />);
    expect(widths().every((width) => width.endsWith('px'))).toBe(true);

    render(
      <DataGrid columns={columns([{ width: 77 }])} rows={rows} rowKey={(r) => r.id} label="Pods" empty={null} />,
    );
    const sized = [...document.querySelectorAll('table.sized col')].map(
      (col) => (col as HTMLElement).style.width,
    );
    expect(sized.length).toBe(2);
    expect(sized.every((width) => width.endsWith('%'))).toBe(true);
  });

  describe('sized, and not sized', () => {
    // An empty list fills its panel through the absence of `sized`, because
    // `theme.css` gives `#panel > .table-scroll > .kt-grid:not(.sized)` a
    // `min-width: 100%`. A React table emits no inline width summed from
    // heading texts, so there is nothing for a `norows` rule to beat.
    it('is not sized when the operator has arranged nothing', () => {
      render(
        <DataGrid columns={columns()} rows={[]} rowKey={(r) => r.id} label="Pods" empty="none" />,
      );
      expect(screen.getByRole('table').classList.contains('sized')).toBe(false);
    });

    it('is sized as soon as one width is stored', () => {
      // The guard, and it is the half that is easy to drop because it asserts an
      // absence: `sized` leaking onto a table nobody arranged takes it to
      // `width: max-content` and stops it filling the panel, and `sized` failing
      // to appear on one the operator dragged undoes the drag while reporting
      // success - which this project has already paid for once.
      expect(isSized(columns([{ width: 77 }]))).toBe(true);
      expect(isSized(columns())).toBe(false);
      expect(isSized(columns([{ width: null }]))).toBe(false);
    });
  });

  it('puts the wrap class on the cell and on nothing inside it', () => {
    // `white-space` inherits, so a wrap class on a child is inert while it
    // reads like a working declaration.
    render(
      <DataGrid
        columns={columns([{}, { wrap: 'prose', cell: (row) => <span>{row.cpu}</span> }])}
        rows={rows}
        rowKey={(r) => r.id}
        label="Pods"
        empty={null}
      />,
    );
    const cell = screen.getAllByRole('cell')[1];
    expect(cell?.classList.contains('prose')).toBe(true);
    expect(cell?.querySelector('.prose')).toBeNull();
    expect(cell?.querySelector('.wrap')).toBeNull();
  });

  it('draws the headings even when there are no rows', () => {
    // An operator looking at an empty list needs to see which columns they
    // asked for. A notice that replaces the table answers a question nobody
    // asked.
    render(
      <DataGrid
        columns={columns()}
        rows={[]}
        rowKey={(r) => r.id}
        label="Pods"
        empty="Nothing matched"
      />,
    );
    expect(screen.getByRole('columnheader', { name: 'Name' })).toBeTruthy();
    expect(screen.getByText('Nothing matched')).toBeTruthy();
  });

  describe('selection', () => {
    const selection = (picked: string[], all = false, some = picked.length > 0) => ({
      picked: new Set(picked),
      toggle: vi.fn(),
      setAll: vi.fn(),
      allPicked: all,
      somePicked: some,
    });

    it('says so when some but not all rows are ticked', () => {
      // `indeterminate` is a DOM property with no attribute behind it, so a
      // component that forgets it draws an empty box over a selection.
      render(
        <DataGrid
          columns={columns()}
          rows={rows}
          rowKey={(r) => r.id}
          label="Pods"
          selection={selection(['a'])}
          empty={null}
        />,
      );
      const header = screen.getByLabelText('Select every row listed') as HTMLInputElement;
      expect(header.indeterminate).toBe(true);
      expect(header.checked).toBe(false);
    });

    it('marks the ticked row so it is not distinguished by color alone', () => {
      render(
        <DataGrid
          columns={columns()}
          rows={rows}
          rowKey={(r) => r.id}
          label="Pods"
          selection={selection(['a'])}
          empty={null}
        />,
      );
      const ticked = screen.getAllByRole('row')[1];
      expect(ticked?.getAttribute('data-picked')).toBe('on');
      expect((within(ticked as HTMLElement).getByLabelText('Select a') as HTMLInputElement).checked).toBe(
        true,
      );
    });

    it('ticks the row without opening it', () => {
      // "One row, one action", and the tick is the control that most needs it:
      // building a selection of six pods otherwise opens and replaces the
      // drawer six times. `ObjectDrawer.test.tsx` asserts this through the
      // whole application; this is the same rule at the grid's own altitude,
      // where `rowAction` is a spy and the answer is a call count rather than
      // a request that did or did not go out.
      const opened = vi.fn();
      const picked = selection([]);
      render(
        <DataGrid
          columns={columns()}
          rows={rows}
          rowKey={(r) => r.id}
          label="Pods"
          selection={picked}
          rowAction={() => opened}
          empty={null}
        />,
      );
      const row = screen.getAllByRole('row')[1] as HTMLElement;

      fireEvent.click(within(row).getByLabelText('Select a'));
      expect(picked.toggle).toHaveBeenCalledWith('a');
      expect(opened).not.toHaveBeenCalled();

      // And the row itself still opens, so the assertion above is about the
      // tick declining and not about an action that was never wired.
      fireEvent.click(row);
      expect(opened).toHaveBeenCalledTimes(1);
    });
  });
});


/**
 * The grip: resizing a column, and moving one.
 *
 * The one that matters most is that a press that moved nothing must store
 * nothing.
 */
/**
 * The redistribution, on its own.
 *
 * A stored width is a share of the table, so the numbers only mean anything
 * beside each other and **the total is the invariant**: it is the table's width,
 * the table fills its panel, and a gesture can therefore only move a boundary
 * between two columns. Everything below is that one sentence checked from a
 * different side.
 */
describe('absorbing a resize', () => {
  const order = ['a', 'b', 'c', 'd'];
  const drawn = { a: 400, b: 200, c: 200, d: 200 };
  const total = (widths: Record<string, number>): number =>
    Math.round(Object.values(widths).reduce((sum, width) => sum + width, 0) * 100) / 100;

  it('keeps the total, whichever way the column is dragged', () => {
    expect(total(absorb(order, drawn, 'a', 500))).toBe(1000);
    expect(total(absorb(order, drawn, 'a', 300))).toBe(1000);
    expect(total(absorb(order, drawn, 'b', 56))).toBe(1000);
  });

  it('takes it from the columns after the dragged one, in proportion to their own width', () => {
    // 100px off `a`, over `b`, `c` and `d` — equal here because they are equal,
    // and that is the point of the next assertion rather than of this one.
    // `closeTo` and not `toEqual`: the arithmetic is floating point and the
    // answer is a third of a hundred, so exact equality would be asserting about
    // IEEE 754 rather than about the redistribution.
    const after = absorb(order, drawn, 'a', 500);
    expect(after['a']).toBeCloseTo(500, 6);
    for (const key of ['b', 'c', 'd']) expect(after[key]).toBeCloseTo(200 - 100 / 3, 6);
  });

  it('leaves the columns before the dragged one exactly where they were', () => {
    // The grip is at the right edge of its own column, so everything to the left
    // of it must not move or the gesture does not track the pointer.
    const after = absorb(order, drawn, 'c', 300);
    expect(after['a']).toBe(400);
    expect(after['b']).toBe(200);
    expect(after['d']).toBe(100);
  });

  it('scales the absorbers rather than sharing the difference equally', () => {
    /*
      Leaving the untouched columns to take equal shares draws them all alike,
      content-blind, Age as wide as Namespace while Namespace shrinks. Scaling
      them all by one factor is the only redistribution that leaves the
      arrangement *between* them exactly as the operator left it.
    */
    const wide = { a: 100, b: 400, c: 100 };
    const after = absorb(['a', 'b', 'c'], wide, 'a', 200);
    expect((after['b'] as number) / (after['c'] as number)).toBeCloseTo(4, 6);
  });

  it('stops at the floor, and hands what one column cannot give to the ones that can', () => {
    // 56 is the grip's floor and the store's `MinWidth`. `b` runs out first and
    // the rest of the demand goes to `c`, which still has room.
    const after = absorb(['a', 'b', 'c'], { a: 100, b: 60, c: 400 }, 'a', 300);
    expect(after['b']).toBe(56);
    expect(total(after)).toBe(560);
    expect(after['a']).toBe(300);
  });

  it('gives the dragged column only what the others can spare', () => {
    // Everything after it is already at the floor, so there is nothing to take
    // and the column stops — rather than pushing three columns to nothing.
    const after = absorb(['a', 'b'], { a: 100, b: 56 }, 'a', 300);
    expect(after).toEqual({ a: 100, b: 56 });
  });

  it('takes from the columns before it when the dragged one is last', () => {
    // The last column has nothing after it. The table shifts under the gesture,
    // which is unavoidable once the total is fixed and better than a last column
    // that cannot be resized at all.
    const after = absorb(order, drawn, 'd', 300);
    expect(total(after)).toBe(1000);
    expect(after['d']).toBe(300);
    expect(after['a']).toBe(400 - 100 / 2);
  });

  it('leaves a column it cannot measure out of the pool', () => {
    // No element to measure: every render before the first paint, and every test
    // in jsdom, which lays nothing out.
    expect(absorb(order, {}, 'a', 500)).toEqual({ a: 500 });
  });
});

/**
 * What is posted, which is the set on screen — scaled where the store's bounds
 * could not hold it, because a clamped number is a changed ratio.
 */
describe('the set that is posted', () => {
  it('posts the numbers on screen when they are all inside the bounds', () => {
    expect(postable(['a', 'b'], { a: 300.4, b: 180.6 })).toEqual([
      { key: 'a', width: 300 },
      { key: 'b', width: 181 },
    ]);
  });

  it('scales a set whose widest column is past the ceiling, instead of letting the store clamp it', () => {
    /*
      On a wide enough panel a column is drawn past `MaxWidth` = 720. Posting it
      would have the store keep 720 and quietly narrow that column's share on the
      next reload — the arrangement changed by a bound rather than by the
      operator. Scaling the whole set changes nothing about the arrangement,
      because the arrangement is the ratios.
    */
    const posted = postable(['a', 'b'], { a: 900, b: 300 });
    expect(posted).toEqual([
      { key: 'a', width: 720 },
      { key: 'b', width: 240 },
    ]);
    expect((posted[0]?.width as number) / (posted[1]?.width as number)).toBe(3);
  });

  it('keeps the floor when both bounds cannot hold at once', () => {
    // Wider than 720/56 = 12.86 to 1 cannot be expressed in the store's bounds at
    // all. A column under the floor is one nobody can grab again; a column at the
    // ceiling is merely compressed.
    const posted = postable(['a', 'b'], { a: 2000, b: 56 });
    expect(posted[1]?.width).toBe(56);
    expect(posted[0]?.width).toBe(720);
  });

  it('leaves out a column with no measurement rather than posting it as automatic', () => {
    expect(postable(['a', 'b'], { b: 100 })).toEqual([{ key: 'b', width: 100 }]);
  });
});

describe('the grip', () => {
  interface Cell {
    id: string;
    name: string;
  }

  const columns: GridColumn<Cell>[] = [
    { key: 'name', label: 'Name', measure: (row) => row.name, cell: (row) => row.name },
    { key: 'status', label: 'Status', measure: () => 'CrashLoopBackOff', cell: () => 'x' },
  ];
  const rows: Cell[] = [{ id: 'a', name: 'storefront-web' }];

  function draw(
    handlers: {
      onResize?: (sizes: readonly { key: string; width: number | null }[]) => void;
      onPlace?: (key: string, target: string, edge: 'before' | 'after') => void;
    } = {},
  ): void {
    render(
      <DataGrid
        columns={columns}
        rows={rows}
        rowKey={(row) => row.id}
        label="Pods"
        empty={<p>nothing</p>}
        {...handlers}
      />,
    );
  }

  function grip(label: string): HTMLElement {
    const found = document.querySelector<HTMLElement>(`[aria-label="Resize the ${label} column"]`);
    if (found === null) throw new Error(`no grip for ${label}`);
    return found;
  }

  /** A pointer gesture that starts at one x and ends at another. */
  function drag(on: HTMLElement, from: number, to: number): void {
    fireEvent.pointerDown(on, { button: 0, clientX: from, pointerId: 1 });
    fireEvent.pointerMove(on, { clientX: to, pointerId: 1 });
    fireEvent.pointerUp(on, { clientX: to, pointerId: 1 });
  }

  it('is a real button, so the keyboard can reach it', () => {
    /*
      **It adds a way, it replaces none.** The arrows resize and Alt with them moves — the path that
      works from the keyboard, and the one that works when a drag on a trackpad
      does not.
    */
    draw({ onResize: () => undefined });
    for (const label of ['Name', 'Status']) {
      const button = grip(label);
      expect(button.tagName).toBe('BUTTON');
      expect(button.getAttribute('aria-label')).toBe(`Resize the ${label} column`);
    }
  });

  it('draws no grip where nothing can be resized', () => {
    // A control that cannot do anything is never drawn, which is the same rule
    // `lockedNote` and the namespace cell are under.
    draw();
    expect(document.querySelector('[data-grip]')).toBeNull();
  });

  it('follows the pointer while it is dragged, and posts the dragged column first', () => {
    /*
      The width is the one thing drawn locally during the gesture, the
      exception to "the server holds the arrangement": the whole point of a
      resize is watching the column follow the pointer. What is drawn is not
      what is kept.

      **One entry here because jsdom measures nothing**, and that is the honest
      reading of this assertion rather than the contract: every rect is zero, so
      `freeze()` has nothing to report and the set is the dragged column alone.
      What the set holds when the headings do have a width is the next two tests,
      with the rects stubbed, and what the browser then draws is not assertable
      here at all — see the Chromium figures on `DataGridProps.onResize`.
    */
    const posted: { key: string; width: number | null }[][] = [];
    draw({ onResize: (sizes) => posted.push([...sizes]) });

    /*
      Read as a share, because the gesture changes the colgroup's **unit**: the
      table is unsized before the pointer goes down and sized for the duration,
      so `before` is in pixels and `during` in percent. Comparing the two raw
      numbers compared a pixel with a percentage — which is how this assertion
      read after `declareWidth` landed, and it failed honestly (57 against 118)
      for a column that had in fact grown.
    */
    const before = shares()[0] ?? 0;
    fireEvent.pointerDown(grip('Name'), { button: 0, clientX: 100, pointerId: 1 });
    fireEvent.pointerMove(grip('Name'), { clientX: 160, pointerId: 1 });

    const during = shares()[0] ?? 0;
    expect(during).not.toBe(before);
    expect(during).toBeGreaterThan(before);

    fireEvent.pointerUp(grip('Name'), { clientX: 160, pointerId: 1 });
    expect(posted.length).toBe(1);
    expect(posted[0]?.[0]?.key).toBe('name');
  });

  /**
   * Widths on the headings, because jsdom lays nothing out.
   *
   * Every `getBoundingClientRect` in this environment is zero: the grid reads
   * the screen and there is no screen. Stubbing the `<th>`s is what lets the *commit* path be asserted —
   * given these drawn widths, this is what the gesture says it decided.
   *
   * **It asserts nothing about layout.** That a table at `width: min-content`
   * then draws each column at the number it was sent is a browser fact, measured
   * in Chromium and quoted on `DataGridProps.onResize`; no test in this file can
   * see it.
   */
  function drawnAt(widths: Record<string, number>): void {
    for (const [label, width] of Object.entries(widths)) {
      const head = [...document.querySelectorAll('thead th')].find(
        (th) => th.textContent?.trim().startsWith(label) === true,
      );
      if (head === undefined) throw new Error(`no heading for ${label}`);
      head.getBoundingClientRect = () =>
        ({ width, height: 28, top: 0, left: 0, right: width, bottom: 28, x: 0, y: 0 }) as DOMRect;
    }
  }

  it('carries every column, and the ones after the dragged column absorb what it gave up', () => {
    /*
      The columns always fill the page, adapting to each other.

      Name gives up 60px and Status takes them, so the total is 480 before and
      480 after. That total is the table's width and the table fills its panel,
      so a gesture that changed it would be moving the table's right edge instead
      of the boundary under the pointer.
    */
    const posted: { key: string; width: number | null }[][] = [];
    draw({ onResize: (sizes) => posted.push([...sizes]) });
    drawnAt({ Name: 300, Status: 180 });

    drag(grip('Name'), 100, 40);

    expect(posted.length).toBe(1);
    expect(posted[0]).toEqual([
      { key: 'name', width: 240 },
      { key: 'status', width: 240 },
    ]);
  });

  it('carries every column when the table is already sized too', () => {
    /*
      **The same set.** A stored width is a share, so after any drag the others
      have all just changed and saying so is the whole of it
      (`DataGridProps.onResize` carries the argument). It is also what keeps
      every column on a table the operator has sized from being re-derived from
      its content, because every one of them is stored.
    */
    const posted: { key: string; width: number | null }[][] = [];
    render(
      <DataGrid
        columns={[{ ...(columns[0] as GridColumn<Cell>), width: 300 }, columns[1] as GridColumn<Cell>]}
        rows={rows}
        rowKey={(row) => row.id}
        label="Pods"
        empty={<p>nothing</p>}
        onResize={(sizes) => posted.push([...sizes])}
      />,
    );
    drawnAt({ Name: 300, Status: 180 });

    drag(grip('Name'), 100, 40);

    expect(posted).toEqual([
      [
        { key: 'name', width: 240 },
        { key: 'status', width: 240 },
      ],
    ]);
  });

  it('posts nothing when the press moved nothing', () => {
    /*
      The sharpest entry on this surface. The grip sits inside the heading, so
      a click landing on it is an ordinary click to the operator — and posting
      then is not a no-op: the server stores what it is sent and a stored width
      is **sticky**, so the column is never sized from its contents again. On a
      list with no rows every column is at its floor, and one stray click would
      freeze the floor into the layout for good.
    */
    const posted: { key: string; width: number | null }[][] = [];
    draw({ onResize: (sizes) => posted.push([...sizes]) });

    // A press and a release with no movement at all.
    fireEvent.pointerDown(grip('Name'), { button: 0, clientX: 100, pointerId: 1 });
    fireEvent.pointerUp(grip('Name'), { clientX: 100, pointerId: 1 });
    expect(posted).toEqual([]);

    // And a drag out and back, which is also a gesture that changed nothing. The
    // Go test compares against the width at pointerdown rather than counting
    // pixels moved, for exactly this case.
    drag(grip('Name'), 100, 100);
    expect(posted).toEqual([]);
  });

  it('commits what is on screen when the pointer is canceled', () => {
    /*
      A touch turning into a scroll, or a window losing focus mid-drag. Leaving it
      uncommitted means the next refresh silently undoes the resize, which reads
      as a feature that works only sometimes.
    */
    const posted: { key: string; width: number | null }[][] = [];
    draw({ onResize: (sizes) => posted.push([...sizes]) });

    fireEvent.pointerDown(grip('Status'), { button: 0, clientX: 200, pointerId: 1 });
    fireEvent.pointerMove(grip('Status'), { clientX: 120, pointerId: 1 });
    fireEvent.pointerCancel(grip('Status'), { clientX: 120, pointerId: 1 });

    expect(posted.length).toBe(1);
    expect(posted[0]?.[0]?.key).toBe('status');
  });

  it('resizes from the keyboard, with a finer step under Shift', () => {
    const posted: { key: string; width: number | null }[][] = [];
    draw({ onResize: (sizes) => posted.push([...sizes]) });

    fireEvent.keyDown(grip('Name'), { key: 'ArrowRight' });
    fireEvent.keyDown(grip('Name'), { key: 'ArrowLeft' });
    fireEvent.keyDown(grip('Name'), { key: 'ArrowRight', shiftKey: true });

    expect(posted.length).toBe(3);
    const base = columnWidth(columns[0] as GridColumn<Cell>, ['storefront-web']);
    // 16 is the step and 4 is the fine one.
    expect(posted[0]?.[0]?.width).toBe(base + 16);
    expect(posted[1]?.[0]?.width).toBe(base - 16);
    expect(posted[2]?.[0]?.width).toBe(base + 4);
  });

  it('moves a column with Alt, as a neighbor and a side', () => {
    /*
      Never as an index. `RailPlaceWrite` carries the argument at length and
      `ColumnPlaceWrite` repeats it: an index is a fact about the list the browser
      last saw, and this panel is redrawn every two seconds.
    */
    const placed: [string, string, string][] = [];
    draw({
      onResize: () => undefined,
      onPlace: (key, target, edge) => placed.push([key, target, edge]),
    });

    fireEvent.keyDown(grip('Name'), { key: 'ArrowRight', altKey: true });
    expect(placed).toEqual([['name', 'status', 'after']]);

    // And nothing off the end: the first column cannot move left.
    fireEvent.keyDown(grip('Name'), { key: 'ArrowLeft', altKey: true });
    expect(placed.length).toBe(1);
  });

  it('does not resize when Alt is held, and does not move without it', () => {
    // The two gestures share the arrow keys, so each must decline the other's.
    const posted: string[] = [];
    const placed: string[] = [];
    draw({
      onResize: (sizes) => posted.push(sizes[0]?.key ?? ''),
      onPlace: (key) => placed.push(key),
    });

    fireEvent.keyDown(grip('Name'), { key: 'ArrowRight', altKey: true });
    expect(posted).toEqual([]);
    expect(placed).toEqual(['name']);

    fireEvent.keyDown(grip('Name'), { key: 'ArrowRight' });
    expect(posted).toEqual(['name']);
    expect(placed).toEqual(['name']);
  });

  it('drops a dragged heading beside the one under the pointer', () => {
    const placed: [string, string, string][] = [];
    draw({ onResize: () => undefined, onPlace: (k, t, e) => placed.push([k, t, e]) });

    const headings = [...document.querySelectorAll('th')];
    const name = headings[0];
    const status = headings[1];
    if (name === undefined || status === undefined) throw new Error('no headings');

    // `draggable` is on the heading and off every control inside it: an anchor or
    // a button is a drag source in its own right and would carry something else
    // into the drop.
    expect(name.getAttribute('draggable')).toBe('true');
    expect(grip('Name').getAttribute('draggable')).toBe('false');

    const transfer = { effectAllowed: '', setData: () => undefined, getData: () => '' };
    fireEvent.dragStart(name, { dataTransfer: transfer });
    // The right half of the target, so the drop is "after".
    status.getBoundingClientRect = () =>
      ({ left: 100, width: 100, right: 200, top: 0, bottom: 0, height: 0, x: 100, y: 0, toJSON: () => ({}) }) as DOMRect;
    fireEvent.dragOver(status, { clientX: 180, dataTransfer: transfer });
    fireEvent.drop(status, { clientX: 180, dataTransfer: transfer });

    expect(placed).toEqual([['name', 'status', 'after']]);
  });

  it('never reorders the table itself', () => {
    /*
      The server holds the arrangement, so a drop sends a
      statement and the answer redraws the table. There is no client-side copy of
      the order to disagree with the file on disk, which is what makes a drag
      against a table redrawn under the pointer land where it was aimed.
    */
    draw({ onResize: () => undefined, onPlace: () => undefined });
    const before = [...document.querySelectorAll('th')].map((th) => th.textContent);

    const headings = [...document.querySelectorAll('th')];
    const transfer = { effectAllowed: '', setData: () => undefined, getData: () => '' };
    fireEvent.dragStart(headings[0] as Element, { dataTransfer: transfer });
    fireEvent.dragOver(headings[1] as Element, { clientX: 180, dataTransfer: transfer });
    fireEvent.drop(headings[1] as Element, { clientX: 180, dataTransfer: transfer });

    expect([...document.querySelectorAll('th')].map((th) => th.textContent)).toEqual(before);
  });
});

describe('what a cut cell or heading offers', () => {
  // jsdom lays nothing out, so the clipping is stated on the element; the
  // layout harness reads it off a real engine.
  function clips(element: Element, by: number): void {
    Object.defineProperty(element, 'clientWidth', { configurable: true, value: 40 });
    Object.defineProperty(element, 'scrollWidth', { configurable: true, value: 40 + by });
  }

  it('adds what a cell draws around its text, rather than flooring it', () => {
    const plain = columnWidth({ key: 'ip', label: 'IP', cell: () => null }, ['192.168.65.3']);
    const padded = columnWidth({ key: 'ip', label: 'IP', pad: 28, cell: () => null }, ['192.168.65.3']);
    expect(padded).toBe(plain + 28);
    // A heavier text costs per character, so a long value gets more than a short.
    const per = (text: string): number =>
      columnWidth({ key: 's', label: 'S', perCharacter: 1, cell: () => null }, [text]) -
      columnWidth({ key: 's', label: 'S', cell: () => null }, [text]);
    expect(per('CreateContainerConfigError')).toBe(26);
    expect(per('Running Running')).toBe(15);
    // And nothing for a column with no text under it: the heading decides.
    expect(columnWidth({ key: 'ip', label: 'IP', pad: 28, cell: () => null }, [])).toBe(
      columnWidth({ key: 'ip', label: 'IP', cell: () => null }, []),
    );
  });

  it('offers the whole text of a cut cell on hover, and nothing for a whole one', () => {
    render(<DataGrid columns={columns()} rows={rows} rowKey={(r) => r.id} label="Pods" empty={null} />);
    const [cut, whole] = [...document.querySelectorAll('tbody tr:first-child td')];
    if (cut === undefined || whole === undefined) throw new Error('no cells');
    clips(cut, 30);
    clips(whole, 0);
    fireEvent.pointerEnter(cut);
    fireEvent.pointerEnter(whole);
    expect(cut.getAttribute('title')).toBe('a-very-long-pod-name-that-would-widen-anything');
    expect(whole.getAttribute('title')).toBeNull();
  });

  it('offers a cut heading its label, and leaves a heading with a sentence of its own alone', () => {
    render(
      <DataGrid
        columns={columns([{}, { headTitle: 'Why this column is empty.' }])}
        rows={rows}
        rowKey={(r) => r.id}
        label="Pods"
        empty={null}
      />,
    );
    const [name, cpu] = [...document.querySelectorAll('thead th')];
    if (name === undefined || cpu === undefined) throw new Error('no headings');
    for (const th of [name, cpu]) clips(th.querySelector('[data-label]') as Element, 20);
    fireEvent.pointerEnter(name);
    fireEvent.pointerEnter(cpu);
    expect(name.getAttribute('title')).toBe('Name');
    expect(cpu.getAttribute('title')).toBeNull();
    expect(cpu.querySelector('[data-label]')?.getAttribute('title')).toBe('Why this column is empty.');
  });
});

describe('the first column held at the left edge of a list', () => {
  it('holds the first heading and cells, opaque, marked for the fade, and no other column', () => {
    render(<DataGrid columns={columns()} rows={rows} rowKey={(r) => r.id} label="Pods" empty={null} stickyHead />);
    const heads = [...document.querySelectorAll('thead th')];
    expect(heads[0]?.className).toContain('left-0');
    expect(heads[0]?.className).toContain('top-0');
    expect(heads[0]?.hasAttribute('data-held')).toBe(true);
    for (const other of heads.slice(1)) {
      expect(other.className).not.toContain('left-0');
      expect(other.hasAttribute('data-held')).toBe(false);
    }
    const row = document.querySelector('tbody tr');
    const cells = [...(row?.querySelectorAll('td') ?? [])];
    expect(cells[0]?.className).toMatch(/\bsticky\b.*\bleft-0\b.*\bbg-bg\b/);
    for (const other of cells.slice(1)) expect(other.className).not.toContain('left-0');
  });

  it('follows the row: the picked surface, the hover surface, and its share of the focus ring', () => {
    const selection = { picked: new Set(['a']), toggle: vi.fn(), setAll: vi.fn(), allPicked: false, somePicked: true };
    render(
      <DataGrid
        columns={columns()}
        rows={rows}
        rowKey={(r) => r.id}
        label="Pods"
        empty={null}
        stickyHead
        selection={selection}
        rowAction={() => () => undefined}
      />,
    );
    const [picked, plain] = [...document.querySelectorAll('tbody tr')].map((row) => row.querySelector('td'));
    expect(picked?.className).toMatch(/\bbg-panel-2\b/);
    expect(picked?.className).not.toMatch(/\bbg-bg\b/);
    expect(plain?.className).toMatch(/\bbg-bg\b/);
    expect(plain?.className).toContain('group-hover:bg-panel-2');
    for (const cell of [picked, plain]) {
      expect(cell?.className).toContain('group-focus-visible:before:border-2');
      expect(cell?.className).toContain('group-focus-visible:before:border-r-0');
      expect(cell?.className).toContain('group-focus-visible:before:border-accent');
    }
  });

  it('neither hovers nor rings a held cell whose row opens nothing', () => {
    render(<DataGrid columns={columns()} rows={rows} rowKey={(r) => r.id} label="Pods" empty={null} stickyHead />);
    const cell = document.querySelector('tbody td');
    expect(cell?.className).not.toContain('group-hover:');
    expect(cell?.className).not.toContain('group-focus-visible:');
  });

  it('holds nothing where the carrying column is not drawn first', () => {
    const [first, ...rest] = columns();
    if (first === undefined) throw new Error('no columns');
    render(<DataGrid columns={[...rest, { ...first, leads: true }]} rows={rows} rowKey={(r) => r.id} label="Pods" empty={null} stickyHead />);
    for (const cell of document.querySelectorAll('th, td')) expect(cell.className).not.toContain('left-0');
  });
});
