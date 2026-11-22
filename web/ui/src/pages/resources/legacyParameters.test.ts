import { describe, expect, it } from 'vitest';

import { modernised } from './legacyParameters';

/**
 * The older `?ns=` and `?q=`, read as the list's `namespace` and `search`.
 * Without that, an old link would open the whole list with nothing saying it
 * had not been narrowed.
 */
describe('an address with the old names', () => {
  it('moves them to the names the list reads, and keeps everything else', () => {
    const next = modernised(new URLSearchParams('ns=platform&q=memory-hog&open=memory-hog&filter=Node%3Ax'));
    expect(next?.getAll('namespace')).toEqual(['platform']);
    expect(next?.get('search')).toBe('memory-hog');
    expect(next?.has('ns')).toBe(false);
    expect(next?.has('q')).toBe(false);
    expect(next?.get('open')).toBe('memory-hog');
    expect(next?.getAll('filter')).toEqual(['Node:x']);
  });

  it('lets the new name win where both are given, and drops the old', () => {
    const next = modernised(new URLSearchParams('ns=old&namespace=new&q=a&search=b'));
    expect(next?.getAll('namespace')).toEqual(['new']);
    expect(next?.get('search')).toBe('b');
    expect(next?.toString()).not.toContain('ns=old');
  });

  it('leaves an address with neither alone', () => {
    expect(modernised(new URLSearchParams('namespace=a&search=b'))).toBeUndefined();
  });
});
