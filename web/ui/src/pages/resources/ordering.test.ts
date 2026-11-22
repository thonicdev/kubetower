import { describe, expect, it } from 'vitest';

import type { Cell, Row } from '../../api';
import { orderRows } from './ordering';

function row(id: string, cells: Record<string, Cell>): Row {
  return { id, cells };
}

describe('ordering rows', () => {
  it('sorts by the server’s sort key and not by the drawn text', () => {
    // The reason `Cell.sort` is in the payload at all: a size column sorts by
    // bytes and not by the string "9 MiB". A client that sorts the text puts
    // 9 MiB after 10 KiB and nothing errors.
    const rows = [
      row('big', { size: { text: '9 MiB', sort: 9_437_184 } }),
      row('small', { size: { text: '10 KiB', sort: 10_240 } }),
    ];
    expect(orderRows(rows, 'size', 'asc').map((r) => r.id)).toEqual(['small', 'big']);
    expect(orderRows(rows, 'size', 'desc').map((r) => r.id)).toEqual(['big', 'small']);
  });

  it('orders names the way an operator reads a list of replicas', () => {
    const rows = [
      row('b', { name: { text: 'pod-10' } }),
      row('a', { name: { text: 'pod-2' } }),
    ];
    expect(orderRows(rows, 'name', 'asc').map((r) => r.id)).toEqual(['a', 'b']);
  });

  it('puts an unmeasured cell last in BOTH directions', () => {
    // The one that matters. Treating a missing reading as zero puts every
    // unmeasured node at the top of an ascending sort by CPU, which reads as a
    // row of idle machines - the exact misreading this product is organized
    // against, arrived at through a sort rather than through a cache.
    const rows = [
      row('busy', { cpu: { text: '900m', reading: { value: 900, trustworthy: true } } }),
      row('unmeasured', { cpu: { text: '—', state: 'absent', reason: { code: 'not_supported_here', message: 'No metrics source.' } } }),
      row('idle', { cpu: { text: '10m', reading: { value: 10, trustworthy: true } } }),
    ];
    expect(orderRows(rows, 'cpu', 'asc').map((r) => r.id)).toEqual(['idle', 'busy', 'unmeasured']);
    expect(orderRows(rows, 'cpu', 'desc').map((r) => r.id)).toEqual(['busy', 'idle', 'unmeasured']);
  });

  it('treats a reading with no sample as no value rather than as zero', () => {
    const rows = [
      row('none', { cpu: { text: '—', reading: { value: null, trustworthy: true } } }),
      row('some', { cpu: { text: '1m', reading: { value: 1, trustworthy: true } } }),
    ];
    expect(orderRows(rows, 'cpu', 'asc').map((r) => r.id)).toEqual(['some', 'none']);
  });

  it('does not reorder the array it was given', () => {
    // The array is the answer in the cache. Sorting it in place would make the
    // next render's unsorted order depend on what was last clicked.
    const rows = [row('b', { name: { text: 'b' } }), row('a', { name: { text: 'a' } })];
    orderRows(rows, 'name', 'asc');
    expect(rows.map((r) => r.id)).toEqual(['b', 'a']);
  });
});
