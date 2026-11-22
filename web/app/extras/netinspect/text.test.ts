import { describe, expect, it } from 'vitest';

import { firstN, restTitle, shortLabel } from './text';

describe('shortLabel', () => {
  it('leaves a short text whole', () => {
    expect(shortLabel('app=web', 20)).toBe('app=web');
  });
  it('keeps the key start and the value end of a long key=value', () => {
    const out = shortLabel('app.kubernetes.io/name=checkout-api', 30);
    expect([...out]).toHaveLength(30);
    expect(out.startsWith('app.kubernetes')).toBe(true);
    expect(out.endsWith('=checkout-api')).toBe(true);
    expect(out).toContain('…');
  });
  it('cuts a long value in the middle when the value alone is too long', () => {
    const out = shortLabel(`a=${'v'.repeat(80)}`, 20);
    expect([...out]).toHaveLength(20);
    expect(out).toContain('…');
  });
  it('keeps the start and the end of other text', () => {
    const out = shortLabel('10.0.0.0/8 and 192.168.0.0/16 except 192.168.1.0/24', 24);
    expect([...out]).toHaveLength(24);
    expect(out.startsWith('10.0.0.0/8')).toBe(true);
    expect(out.endsWith('1.0/24')).toBe(true);
  });
  it('never exceeds a tiny max', () => {
    expect(shortLabel('abcdef', 1)).toBe('…');
    expect([...shortLabel('abcdef', 2)].length).toBeLessThanOrEqual(2);
  });
});

describe('firstN', () => {
  it('splits shown from rest', () => {
    expect(firstN([1, 2, 3, 4, 5], 3)).toEqual({ shown: [1, 2, 3], rest: [4, 5] });
    expect(firstN([1], 3)).toEqual({ shown: [1], rest: [] });
  });
  it('lists the rest one per line', () => {
    expect(restTitle(['a', 'b'])).toBe('a\nb');
  });
});
