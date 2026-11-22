import { describe, expect, it } from 'vitest';

import { columnWidth, type GridColumn } from '../../components/DataGrid';
import { STATUS_FLOOR_TEXT, withStatusFloor } from './statusFloor';

describe('the status column', () => {
  const status: GridColumn<{ text: string }> = {
    key: 'status',
    label: 'Status',
    measure: (row) => row.text,
    cell: (row) => row.text,
  };

  it('declares room for CrashLoopBackOff in full, even on a page of short statuses', () => {
    const floored = withStatusFloor(status);
    const full = columnWidth({ ...status, reserve: 0 }, [STATUS_FLOOR_TEXT]);
    expect(floored.reserve).toBe(full);
    expect(columnWidth(floored, ['Running'])).toBeGreaterThanOrEqual(full);
  });

  it('floors the status column under the key the server sends', () => {
    const served = withStatusFloor({ ...status, key: 'status-3' });
    expect(served.reserve).toBe(columnWidth({ ...status, reserve: 0 }, [STATUS_FLOOR_TEXT]));
  });

  it('floors it at any position, two digits included', () => {
    const served = withStatusFloor({ ...status, key: 'status-12' });
    expect(served.reserve).toBe(columnWidth({ ...status, reserve: 0 }, [STATUS_FLOOR_TEXT]));
  });

  it('does not floor a column whose label only starts with status', () => {
    const other: GridColumn<{ text: string }> = { ...status, key: 'statuses-12' };
    expect(withStatusFloor(other)).toBe(other);
  });

  it('leaves every other column as it is', () => {
    const age: GridColumn<{ text: string }> = { ...status, key: 'age', label: 'Age' };
    expect(withStatusFloor(age)).toBe(age);
  });
});
