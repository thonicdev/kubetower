import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { DataGrid, type GridColumn } from './DataGrid';
import { LIST_PAGE, Windowed } from './Windowed';

interface Row {
  id: string;
}

const many = (n: number): Row[] => Array.from({ length: n }, (_, i) => ({ id: `row-${String(i)}` }));

const columns: GridColumn<Row>[] = [{ key: 'id', label: 'Id', measure: (r) => r.id, cell: (r) => r.id }];

describe('one page size for every list', () => {
  it('is 100', () => {
    expect(LIST_PAGE).toBe(100);
  });

  it('a grid is not paged: it offers no button and draws a bounded window', () => {
    const { container } = render(<DataGrid columns={columns} rows={many(5000)} rowKey={(r) => r.id} label="things" empty="none" />);
    expect(container.querySelector('[data-more]')).toBeNull();
    expect(container.querySelectorAll('tbody tr:not([data-spacer])').length).toBeLessThan(LIST_PAGE);
  });

  it('a Windowed list of 250 draws 100, then 200, then the remainder', () => {
    const { container } = render(
      <ul>
        <Windowed items={many(250)} more={(b) => <li>{b}</li>}>
          {(page) => page.map((r) => <li key={r.id}>{r.id}</li>)}
        </Windowed>
      </ul>,
    );
    expect(container.querySelectorAll('li').length).toBe(101);
    fireEvent.click(screen.getByText('Show 100 more of 150'));
    expect(container.querySelectorAll('li').length).toBe(201);
    fireEvent.click(screen.getByText('Show 50 more of 50'));
    expect(container.querySelectorAll('li').length).toBe(250);
    expect(container.querySelector('[data-more]')).toBeNull();
  });
});
