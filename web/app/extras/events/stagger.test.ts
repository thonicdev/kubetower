import { describe, expect, it } from 'vitest';

import { staggered } from './parts';

type Mark = { id: string; at: number; width: number; flip: boolean };

/** Where a mark's label sits along the axis, in percent. */
const span = (m: Mark): [number, number] => (m.flip ? [m.at - m.width, m.at] : [m.at, m.at + m.width]);

describe('staggered', () => {
  const marks: Mark[] = [
    { id: 'a', at: 10, width: 14, flip: false },
    { id: 'b', at: 12, width: 14, flip: false },
    { id: 'c', at: 14, width: 14, flip: false },
    { id: 'd', at: 60, width: 14, flip: false },
    { id: 'e', at: 90, width: 14, flip: true },
    { id: 'f', at: 92, width: 14, flip: true },
  ];

  it('is the same for the same marks, in any order', () => {
    const one = staggered(marks);
    const two = staggered([...marks].reverse());
    expect([...one].sort()).toEqual([...two].sort());
  });

  it('never lets two labels share a row where they overlap', () => {
    const rows = staggered(marks);
    for (const m of marks) {
      for (const n of marks) {
        if (m.id >= n.id || rows.get(m.id) !== rows.get(n.id)) continue;
        const [a, b] = span(m);
        const [c, d] = span(n);
        expect(a < d && c < b).toBe(false);
      }
    }
  });

  it('never lets a change line cross another label below its own, where a row allows it', () => {
    const clean = marks.filter((m) => !m.flip);
    const rows = staggered(clean);
    for (const m of clean) {
      for (const n of clean) {
        if (m.id === n.id || (rows.get(m.id) ?? 0) >= (rows.get(n.id) ?? 0)) continue;
        const [a, b] = span(n);
        expect(a <= m.at && m.at <= b).toBe(false);
      }
    }
  });

  it('ends, and still keeps labels apart, when no row avoids a line', () => {
    // f's label, flipped left, covers e's line whatever row it takes.
    const rows = staggered(marks.filter((m) => m.flip));
    expect(rows.get('e')).toBe(0);
    expect(rows.get('f')).toBe(1);
  });

  it('keeps a lone mark on the first row', () => {
    expect(staggered([{ id: 'x', at: 50, width: 10, flip: false }]).get('x')).toBe(0);
  });
});
