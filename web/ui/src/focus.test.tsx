import { QueryClientProvider } from '@tanstack/react-query';
import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { DataGrid, type GridColumn } from './components/DataGrid';
import { FilterBox } from './components/FilterBox';
import { SelectionBar } from './components/SelectionBar';
import { createQueryClient } from './query/client';
import { Failure } from './state/StateNotice';
import {
  focusRing,
  focusRingClear,
  focusRingFlush,
  focusRingInside,
  focusRingTight,
} from './focus';

/**
 * What these tests can and cannot say.
 *
 * **jsdom computes no layout and no styles**, so nothing here proves a ring is
 * visible, or 2px, or blue. A test that claimed to would be reporting a check
 * it did not perform.
 *
 * What it does prove is the failure that matters: a control that loses the
 * class in a refactor, or gains two of the three utilities. So these pin the
 * class onto the element, and the tabstop onto the controls that must have one.
 *
 * The ring itself is measured in a browser against the built stylesheet, not
 * faked here.
 */

interface Thing {
  id: string;
  name: string;
}

const rows: Thing[] = [
  { id: 'a', name: 'alpha' },
  { id: 'b', name: 'beta' },
];

const columns: GridColumn<Thing>[] = [
  { key: 'name', label: 'Name', measure: (row) => row.name, cell: (row) => row.name },
];

/** The three utilities, as a set, so an element carrying two of them fails. */
function carries(element: Element, ring: string): boolean {
  const has = new Set(element.className.split(/\s+/));
  return ring.split(' ').every((utility) => has.has(utility));
}

describe('the ring is one string and it says all three things', () => {
  it.each([
    ['focusRing', focusRing, '2px'],
    ['focusRingTight', focusRingTight, '1px'],
    ['focusRingFlush', focusRingFlush, '-2px'],
    ['focusRingInside', focusRingInside, '-1px'],
    ['focusRingClear', focusRingClear, '3px'],
  ])('%s carries the width, the color and an offset', (_name, ring) => {
    // Two of the three is the failure this exists to make impossible: an
    // `outline-2` with no color draws the UA's, and a color with no width
    // draws nothing at all.
    expect(ring).toContain('focus-visible:outline-2');
    expect(ring).toContain('focus-visible:outline-accent');
    expect(ring).toMatch(/focus-visible:-?outline-offset-\d/);
  });

  it('never uses the bare :focus pseudo-class', () => {
    // A ring that fires on a click is one a person stops seeing.
    for (const ring of [focusRing, focusRingTight, focusRingFlush, focusRingInside, focusRingClear]) {
      for (const utility of ring.split(' ')) {
        expect(utility.startsWith('focus-visible:')).toBe(true);
      }
    }
  });
});

describe('the grid', () => {
  it('rings the row, and only where the row opens something', () => {
    const { rerender } = render(
      <DataGrid
        columns={columns}
        rows={rows}
        rowKey={(row) => row.id}
        label="Things"
        empty={null}
        rowAction={() => () => undefined}
      />,
    );

    const row = document.querySelector('tbody tr');
    expect(row).not.toBeNull();
    expect(row?.getAttribute('tabindex')).toBe('0');
    expect(carries(row as Element, focusRingFlush)).toBe(true);

    // A row that opens nothing is not a tabstop, so it must not advertise one.
    // The same defect the `rowAction` shape was introduced for.
    rerender(
      <DataGrid columns={columns} rows={rows} rowKey={(row) => row.id} label="Things" empty={null} />,
    );
    const inert = document.querySelector('tbody tr');
    expect(inert?.getAttribute('tabindex')).toBeNull();
    expect(carries(inert as Element, focusRingFlush)).toBe(false);
  });

  it('rings the sort control at +3, clear of the cell padding', () => {
    render(
      <DataGrid
        columns={columns}
        rows={rows}
        rowKey={(row) => row.id}
        label="Things"
        empty={null}
        onSort={() => undefined}
      />,
    );
    const sort = screen.getByRole('button', { name: /name/i });
    expect(carries(sort, focusRingClear)).toBe(true);
  });

  it('rings the grip, which is a real button and has to stay reachable', () => {
    render(
      <DataGrid
        columns={columns}
        rows={rows}
        rowKey={(row) => row.id}
        label="Things"
        empty={null}
        onResize={() => undefined}
      />,
    );
    const grip = document.querySelector('[data-grip]');
    expect(grip).not.toBeNull();
    expect(carries(grip as Element, focusRingTight)).toBe(true);
  });

  it('rings both checkboxes', () => {
    render(
      <DataGrid
        columns={columns}
        rows={rows}
        rowKey={(row) => row.id}
        label="Things"
        empty={null}
        selection={{
          picked: new Set<string>(),
          allPicked: false,
          somePicked: false,
          toggle: () => undefined,
          setAll: () => undefined,
        }}
      />,
    );
    const boxes = document.querySelectorAll('input[type="checkbox"]');
    expect(boxes.length).toBe(3); // one heading, two rows
    for (const box of boxes) expect(carries(box, focusRing)).toBe(true);
  });

  it('puts the row before its own cell controls in the tab order', () => {
    // The row IS the control (`rowAction`), and a cell control inside it is a
    // second one. Source order is the tab order here - nothing sets a positive
    // tabIndex anywhere - so this pins that nothing ever does.
    render(
      <DataGrid
        columns={columns}
        rows={rows}
        rowKey={(row) => row.id}
        label="Things"
        empty={null}
        rowAction={() => () => undefined}
        selection={{
          picked: new Set<string>(),
          allPicked: false,
          somePicked: false,
          toggle: () => undefined,
          setAll: () => undefined,
        }}
      />,
    );
    const positive = [...document.querySelectorAll('[tabindex]')].filter(
      (element) => Number(element.getAttribute('tabindex')) > 0,
    );
    expect(positive).toEqual([]);

    const row = document.querySelector('tbody tr') as HTMLElement;
    const box = row.querySelector('input[type="checkbox"]') as HTMLElement;
    expect(row.compareDocumentPosition(box) & Node.DOCUMENT_POSITION_CONTAINED_BY).toBeTruthy();
  });
});

describe('the fields and the notices', () => {
  it('draws the filter box ring inside and clears the border under it', () => {
    render(
      <FilterBox name="filter" label="Filter" value="" onChange={() => undefined} />,
    );
    const box = screen.getByLabelText('Filter');
    expect(carries(box, focusRingInside)).toBe(true);
    // Without this the field has a border AND a ring one pixel inside it, which
    // reads as two borders rather than as focus. `.filter`, app.css:293.
    expect(box.className).toContain('focus-visible:border-transparent');
  });

  it('rings the retry button on a failure', () => {
    render(<Failure error={new Error('nope')} onRetry={() => undefined} />);
    const retry = screen.getByRole('button', { name: /try again/i });
    expect(carries(retry, focusRingTight)).toBe(true);
  });

  it('rings the selection bar’s control', () => {
    render(
      // The bar previews and acts, so it needs a query client. Mounting it bare
      // threw once the verbs arrived, which is the shape of every "a component
      // grew a dependency" break: the test that renders it alone is the one that
      // finds out first.
      <QueryClientProvider client={createQueryClient()}>
        <SelectionBar
          selection={{
            picked: new Set(['a']),
            count: 1,
            dropped: 0,
            allPicked: false,
            somePicked: true,
            toggle: () => undefined,
            setAll: () => undefined,
            clear: () => undefined,
          }}
          cluster="prod"
          kind="pods"
          targetsFor={() => []}
        />
      </QueryClientProvider>,
    );
    // Clear is a bordered button (`btnSecondary`), so it takes the tight ring
    // every bordered button takes: at +2 the gap reads as a second border.
    const clear = screen.getByRole('button', { name: 'Clear' });
    expect(carries(clear, focusRingTight)).toBe(true);
  });
});
