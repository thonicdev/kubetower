import { describe, expect, it } from 'vitest';

import { btnPrimary, btnToggle } from './buttons';

/** The utilities of a class string, as a set. */
function classes(text: string): Set<string> {
  return new Set(text.split(/\s+/).filter((one) => one !== ''));
}

describe('the primary and a pressed toggle', () => {
  it('do not look alike: the primary is filled, a pressed toggle is not', () => {
    const primary = classes(btnPrimary);
    const pressed = classes(btnToggle(true));
    expect(btnToggle(true)).not.toBe(btnPrimary);
    expect(primary.has('bg-accent')).toBe(true);
    expect([...pressed].some((one) => one.startsWith('bg-'))).toBe(false);
    // Both carry the accent border; the fill is the whole difference, so it
    // is the thing pinned.
    expect(pressed.has('border-accent')).toBe(true);
  });

  it('draws a toggle that is not pressed as the usual button', () => {
    const off = classes(btnToggle(false));
    expect(off.has('border-line')).toBe(true);
    expect(off.has('border-accent')).toBe(false);
  });
});
