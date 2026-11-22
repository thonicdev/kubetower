import { act, fireEvent, render, screen } from '@testing-library/react';
import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';

import { DataGrid, type GridColumn, type GridSort } from './DataGrid';
import { WINDOW_AFTER } from './useRowWindow';

/**
 * A long list is drawn through a window: every row is in the list, the ones in
 * view are in the page. jsdom lays nothing out, so these tests give the scroll
 * container the size and the offsets the window reads: a scroller 720px high,
 * and a body whose top moves with `scrollTop`.
 */

interface Pod {
  id: string;
  name: string;
  cpu: number;
}

const HEIGHT = 720;
const ROW = 36;

function pods(count: number): Pod[] {
  return Array.from({ length: count }, (_, at) => ({
    id: `pod-${String(at).padStart(5, '0')}`,
    name: `pod-${String(at).padStart(5, '0')}`,
    cpu: at,
  }));
}

const columns: GridColumn<Pod>[] = [
  { key: 'name', label: 'Name', leads: true, sortable: true, measure: (p) => p.name, cell: (p) => p.name },
  {
    key: 'cpu',
    label: 'CPU',
    align: 'end',
    sortable: true,
    measure: (p) => `${String(p.cpu)}m`,
    cell: (p) => `${String(p.cpu)}m`,
  },
];

function box(top: number, height: number): DOMRect {
  return { top, bottom: top + height, left: 0, right: 0, width: 0, height, x: 0, y: top, toJSON: () => ({}) };
}

/** Gives the scroller its box, and the body a top that follows `scrollTop`. */
function lay(scroller: HTMLElement, state: { top: number }): void {
  Object.defineProperty(scroller, 'clientHeight', { configurable: true, get: () => HEIGHT });
  scroller.getBoundingClientRect = () => box(0, HEIGHT);
  const body = scroller.querySelector('tbody') as HTMLElement;
  body.getBoundingClientRect = () => box(-state.top, 0);
}

async function frames(): Promise<void> {
  await act(async () => {
    await new Promise((done) => requestAnimationFrame(() => done(null)));
  });
}

async function scrollTo(scroller: HTMLElement, state: { top: number }, top: number): Promise<void> {
  state.top = top;
  fireEvent.scroll(scroller);
  await frames();
}

function Harness({ count, menu }: { count: number; menu?: (id: string) => void }) {
  const [picked, setPicked] = useState<ReadonlySet<string>>(new Set());
  const [sort, setSort] = useState<GridSort | undefined>(undefined);
  const [filter, setFilter] = useState('');
  let rows = pods(count).filter((p) => p.name.includes(filter));
  if (sort !== undefined) rows = [...rows].sort((a, b) => (sort.direction === 'asc' ? a.cpu - b.cpu : b.cpu - a.cpu));
  return (
    <div id="scroller" style={{ overflowY: 'auto' }}>
      <input
        aria-label="filter"
        value={filter}
        onChange={(event) => {
          setFilter(event.target.value);
        }}
      />
      <span data-count>{String(rows.length)}</span>
      <DataGrid
        columns={columns}
        rows={rows}
        rowKey={(p) => p.id}
        label="Pods"
        empty={null}
        stickyHead
        sort={sort}
        onSort={(key) => {
          setSort((s) => ({ key, direction: s?.key === key && s.direction === 'asc' ? 'desc' : 'asc' }));
        }}
        selection={{
          picked,
          toggle: (id) => {
            setPicked((held) => {
              const next = new Set(held);
              if (next.has(id)) next.delete(id);
              else next.add(id);
              return next;
            });
          },
          setAll: (on) => {
            setPicked(on ? new Set(rows.map((p) => p.id)) : new Set());
          },
          allPicked: picked.size === rows.length && rows.length > 0,
          somePicked: picked.size > 0,
        }}
        rowAction={(p) => () => {
          void p;
        }}
        {...(menu === undefined
          ? {}
          : {
              rowMenu: (p: Pod) => () => {
                menu(p.id);
              },
            })}
      />
      <span data-ticked>{String(picked.size)}</span>
    </div>
  );
}

function bodyRows(): HTMLElement[] {
  return [...document.querySelectorAll<HTMLElement>('tbody tr:not([data-spacer])')];
}

function ids(): (string | null)[] {
  return bodyRows().map((row) => row.getAttribute('data-rowid'));
}

async function mount(count: number, menu?: (id: string) => void) {
  const state = { top: 0 };
  render(<Harness count={count} {...(menu === undefined ? {} : { menu })} />);
  const scroller = document.getElementById('scroller') as HTMLElement;
  lay(scroller, state);
  await scrollTo(scroller, state, 0);
  return { scroller, state };
}

describe('a long list drawn through a window', () => {
  it('draws a short list whole', async () => {
    await mount(WINDOW_AFTER);
    expect(bodyRows()).toHaveLength(WINDOW_AFTER);
    expect(document.querySelector('[data-spacer]')).toBeNull();
  });

  it('draws a bounded number of rows of 5000, and measures the time', async () => {
    const started = performance.now();
    await mount(5000);
    const took = performance.now() - started;
    console.log(`5000 rows: ${String(bodyRows().length)} <tr> drawn, ${took.toFixed(0)} ms`);
    expect(bodyRows().length).toBeLessThan(100);
    expect(bodyRows().length).toBeGreaterThan(20);
    expect(took).toBeLessThan(3000);
    expect(document.querySelector('[data-count]')?.textContent).toBe('5000');
    expect(document.querySelector('[data-more]')).toBeNull();
  });

  it('keeps the total height whole with spacers, and moves the window on scroll', async () => {
    const { scroller, state } = await mount(5000);
    const total = (): number =>
      [...document.querySelectorAll<HTMLElement>('tbody tr[data-spacer] td')].reduce(
        (sum, td) => sum + Number.parseFloat(td.style.height),
        0,
      ) +
      bodyRows().length * ROW;
    expect(total()).toBe(5000 * ROW);
    await scrollTo(scroller, state, 2500 * ROW);
    expect(total()).toBe(5000 * ROW);
    expect(ids()).toContain('pod-02500');
    expect(ids()).toContain('pod-02519');
    expect(ids()).not.toContain('pod-00000');
    await scrollTo(scroller, state, 5000 * ROW - HEIGHT);
    expect(ids()).toContain('pod-04999');
  });

  it('sizes the columns from every row, not the ones drawn', async () => {
    const widths = (all: Pod[]): number => {
      const { unmount } = render(
        <DataGrid columns={columns} rows={all} rowKey={(p) => p.id} label="Pods" empty={null} />,
      );
      const first = (document.querySelector('col') as HTMLElement).style.width;
      unmount();
      return Number.parseFloat(first);
    };
    const all = pods(5000);
    const plain = widths(all);
    all[4999] = { id: 'pod-04999', name: 'a-pod-with-an-extraordinarily-long-name-at-the-very-end', cpu: 1 };
    expect(widths(all)).toBeGreaterThan(plain);
  });

  it('sorts and filters the whole list before windowing', async () => {
    const { scroller, state } = await mount(5000);
    fireEvent.click(screen.getByRole('button', { name: /^CPU/ }));
    fireEvent.click(screen.getByRole('button', { name: /^CPU/ }));
    await scrollTo(scroller, state, 0);
    expect(ids()[0]).toBe('pod-04999');
    fireEvent.change(screen.getByLabelText('filter'), { target: { value: 'pod-0123' } });
    expect(document.querySelector('[data-count]')?.textContent).toBe('10');
    expect(bodyRows()).toHaveLength(10);
  });

  it('select-all picks every filtered row, drawn or not', async () => {
    await mount(5000);
    fireEvent.click(screen.getByRole('checkbox', { name: 'Select every row listed' }));
    expect(document.querySelector('[data-ticked]')?.textContent).toBe('5000');
    expect(
      [...document.querySelectorAll<HTMLInputElement>('tbody input[type="checkbox"]')].every((one) => one.checked),
    ).toBe(true);
  });

  it('opens a row menu from the button and from a right-click', async () => {
    const opened = vi.fn();
    const { scroller, state } = await mount(5000, opened);
    await scrollTo(scroller, state, 1000 * ROW);
    fireEvent.click(screen.getByLabelText('Actions on pod-01005'));
    fireEvent.contextMenu(screen.getByLabelText('Select pod-01006'), { button: 2, clientX: 5, clientY: 5 });
    expect(opened.mock.calls).toEqual([['pod-01005'], ['pod-01006']]);
  });

  it('keeps the focused row in the page while it is scrolled out of view', async () => {
    const { scroller, state } = await mount(5000);
    const row = document.querySelector<HTMLElement>('tr[data-rowid="pod-00003"]') as HTMLElement;
    act(() => {
      row.focus();
    });
    expect(document.activeElement).toBe(row);
    await scrollTo(scroller, state, 3000 * ROW);
    await frames();
    expect(document.querySelector('tr[data-rowid="pod-00003"]')).toBe(row);
    expect(document.activeElement).toBe(row);
    // The focused row and the far window are both drawn, in list order, so Tab
    // from the focused row moves on to the rows in view.
    expect(ids()[0]).toBe('pod-00003');
    expect(ids()).toContain('pod-03000');
    // Moving the focus out of the table lets the row go.
    act(() => {
      (screen.getByLabelText('filter') as HTMLElement).focus();
    });
    expect(document.querySelector('tr[data-rowid="pod-00003"]')).toBeNull();
  });
});
